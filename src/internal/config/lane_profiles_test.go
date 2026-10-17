package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"remote-session-runner/src/internal/domain"
)

func TestLanesL102MacProfilesContextsAndCredentialBindingsResolveWithinLane(t *testing.T) {
	for _, lane := range []ReleaseLane{ReleaseLaneDev, ReleaseLaneQAS, ReleaseLanePRD} {
		bindings := l102MacBindings(t, lane)
		contents, err := yaml.Marshal(bindings)
		if err != nil {
			t.Fatal(err)
		}
		validated, err := ParseV3ProfileBindings(contents)
		if err != nil {
			t.Fatalf("parse Mac %s test-identity bindings: %v\n%s", lane, err, contents)
		}
		if validated.Host() != HostKindMac || validated.Lane() != lane {
			t.Fatalf("Mac identity = %s/%s, want mac/%s", validated.Host(), validated.Lane(), lane)
		}
		if got, want := validated.SortedV3Profiles(), []string{"factory-sandbox", "linux-host", "sandbox-host"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("Mac %s remote profiles = %v, want %v", lane, got, want)
		}
		if err := ValidateV3MacProfileDistinctness(validated); err != nil {
			t.Fatalf("Mac %s reused profile credentials: %v", lane, err)
		}
		for _, profile := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
			route, ok := validated.MacProfile(profile)
			if !ok {
				t.Fatalf("Mac %s route %s is missing", lane, profile)
			}
			namespace, _ := DeriveLaneNamespace(HostKindLinux, lane, profile)
			if route.Queued.Port != namespace.QueuePort || route.Direct.Name != namespace.DirectEndpointName || route.Direct.URL != namespace.DirectEndpointURL {
				t.Fatalf("Mac %s/%s route escaped derived endpoints: %+v", lane, profile, route)
			}
			target := V3TargetBinding{Kind: domain.TargetKindRemote, Profile: profile}
			context, ok := validated.Resolve(profile+"-dev", target)
			if !ok || context.Target != target {
				t.Fatalf("Mac %s pair %s/%s did not resolve: %+v, %v", lane, profile+"-dev", profile, context, ok)
			}
			if _, err := os.Stat(route.Queued.DispatcherPrivateKeyFile); !os.IsNotExist(err) {
				t.Fatalf("test credential unexpectedly exists at %q (stat error %v)", route.Queued.DispatcherPrivateKeyFile, err)
			}
		}
		if lane == ReleaseLanePRD {
			if _, ok := validated.Resolve("mac-dev", V3TargetBinding{Kind: domain.TargetKindLocal, Profile: "mac-workstation"}); !ok {
				t.Fatal("PRD Mac local compatibility pair did not resolve")
			}
		}
	}
}

func TestLanesL102LinuxProfileAndMacLinuxCredentialsArePaired(t *testing.T) {
	for _, lane := range []ReleaseLane{ReleaseLaneDev, ReleaseLaneQAS, ReleaseLanePRD} {
		for _, profile := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
			mac, err := ValidateV3ProfileBindings(l102MacBindings(t, lane))
			if err != nil {
				t.Fatalf("validate Mac %s: %v", lane, err)
			}
			linuxBindings := l102LinuxBindings(t, lane, profile, mac)
			contents, err := yaml.Marshal(linuxBindings)
			if err != nil {
				t.Fatal(err)
			}
			linux, err := ParseV3ProfileBindings(contents)
			if err != nil {
				t.Fatalf("parse Linux %s/%s test-identity bindings: %v\n%s", lane, profile, err, contents)
			}
			if linux.Host() != HostKindLinux || linux.RemoteProfile() != profile || linux.Lane() != lane {
				t.Fatalf("Linux identity = %s/%s/%s, want linux/%s/%s", linux.Host(), linux.Lane(), linux.RemoteProfile(), lane, profile)
			}
			if err := ValidateV3MacLinuxProfilePair(mac, linux, profile); err != nil {
				t.Fatalf("Mac/Linux %s/%s credential pair rejected: %v", lane, profile, err)
			}
			if len(linux.ExecutionContexts()) != 1 {
				t.Fatalf("Linux %s/%s has %d contexts, want the one local profile", lane, profile, len(linux.ExecutionContexts()))
			}
			context, ok := linux.Resolve(profile+"-dev", V3TargetBinding{Kind: domain.TargetKindRemote, Profile: profile})
			if !ok || context.Target.Profile != profile {
				t.Fatalf("Linux %s/%s pair did not resolve: %+v, %v", lane, profile, context, ok)
			}
			if _, ok := linux.Resolve(profile+"-dev", V3TargetBinding{Kind: domain.TargetKindRemote, Profile: "linux-host"}); profile != "linux-host" && ok {
				t.Fatalf("Linux %s/%s accepted foreign local profile", lane, profile)
			}
		}
	}
}

func TestLanesL102RejectsCrossLaneCredentialReuse(t *testing.T) {
	var configs []ValidatedV3Profiles
	for _, lane := range []ReleaseLane{ReleaseLaneDev, ReleaseLaneQAS, ReleaseLanePRD} {
		validated, err := ValidateV3ProfileBindings(l102MacBindings(t, lane))
		if err != nil {
			t.Fatalf("validate %s test identities: %v", lane, err)
		}
		configs = append(configs, validated)
	}
	if err := ValidateV3MacLaneCredentialSeparation(configs...); err != nil {
		t.Fatalf("distinct test identities across lanes rejected: %v", err)
	}

	qasBindings := l102MacBindings(t, ReleaseLaneQAS)
	qasBindings.MacProfiles[0].Direct.ClientCertificateFingerprint = configs[0].profiles["linux-host"].Direct.ClientCertificateFingerprint
	qas, err := ValidateV3ProfileBindings(qasBindings)
	if err != nil {
		t.Fatalf("individual QAS config should remain structurally valid: %v", err)
	}
	configs[1] = qas
	if err := ValidateV3MacLaneCredentialSeparation(configs...); !errors.Is(err, ErrInvalidLaneProfiles) {
		t.Fatalf("cross-lane client certificate reuse error = %v, want ErrInvalidLaneProfiles", err)
	}

	qasBindings = l102MacBindings(t, ReleaseLaneQAS)
	qasBindings.MacProfiles[0].Direct.ExpectedClientCAFingerprint = configs[0].profiles["linux-host"].Direct.ExpectedClientCAFingerprint
	qas, err = ValidateV3ProfileBindings(qasBindings)
	if err != nil {
		t.Fatalf("individual QAS config with a copied CA assertion should remain structurally valid: %v", err)
	}
	configs[1] = qas
	if err := ValidateV3MacLaneCredentialSeparation(configs...); !errors.Is(err, ErrInvalidLaneProfiles) {
		t.Fatalf("cross-lane client CA reuse error = %v, want ErrInvalidLaneProfiles", err)
	}
}

func TestLanesL102RejectsCrossLaneRoutesAndUnsafeCredentialBindings(t *testing.T) {
	base := l102MacBindings(t, ReleaseLaneDev)
	mutations := map[string]func(*V3ProfileBindings){
		"missing accepted profile": func(value *V3ProfileBindings) { value.MacProfiles = value.MacProfiles[:2] },
		"foreign endpoint port":    func(value *V3ProfileBindings) { value.MacProfiles[0].Direct.URL = "https://129.151.232.40:8445" },
		"foreign profile address":  func(value *V3ProfileBindings) { value.MacProfiles[0].Queued.Host = "132.226.205.205" },
		"caller credential path":   func(value *V3ProfileBindings) { value.MacProfiles[0].Direct.ClientPrivateKeyFile = "/tmp/foreign.key" },
		"invalid fingerprint": func(value *V3ProfileBindings) {
			value.MacProfiles[0].Direct.ClientCertificateFingerprint = "sha256:short"
		},
		"foreign lane controller SAN": func(value *V3ProfileBindings) {
			value.MacProfiles[0].Direct.ClientURISAN = laneControllerURI(ReleaseLaneQAS, value.MacProfiles[0].Profile)
		},
		"wrong controller ID": func(value *V3ProfileBindings) {
			value.MacProfiles[0].Direct.ControllerID = "runner-controller-qas-linux-host"
		},
		"duplicate credential fingerprint": func(value *V3ProfileBindings) {
			value.MacProfiles[1].Queued.DispatcherPublicKeyFingerprint = value.MacProfiles[0].Queued.DispatcherPublicKeyFingerprint
		},
		"environment points at foreign profile": func(value *V3ProfileBindings) { value.Environments[0].AllowedTargets[0].Profile = "unknown-host" },
		"missing execution context":             func(value *V3ProfileBindings) { value.ExecutionContexts = value.ExecutionContexts[:2] },
		"unpermitted execution context":         func(value *V3ProfileBindings) { value.ExecutionContexts[0].Target.Profile = "sandbox-host" },
		"development local target": func(value *V3ProfileBindings) {
			value.Environments[0].AllowedTargets[0] = V3TargetBinding{Kind: domain.TargetKindLocal, Profile: "mac-workstation"}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := cloneV3ProfileBindings(base)
			mutate(&candidate)
			if _, err := ValidateV3ProfileBindings(candidate); !errors.Is(err, ErrInvalidLaneProfiles) {
				t.Fatalf("invalid profile binding error = %v, want ErrInvalidLaneProfiles", err)
			}
		})
	}

	prD := l102MacBindings(t, ReleaseLanePRD)
	for i := range prD.Environments {
		if prD.Environments[i].Name == "mac-dev" {
			prD.Environments = append(prD.Environments[:i], prD.Environments[i+1:]...)
			break
		}
	}
	prD.ExecutionContexts = filterContexts(prD.ExecutionContexts, "mac-dev")
	if _, err := ValidateV3ProfileBindings(prD); !errors.Is(err, ErrInvalidLaneProfiles) {
		t.Fatalf("PRD without its local compatibility context error = %v", err)
	}

	linux := l102LinuxBindings(t, ReleaseLaneDev, "linux-host", mustValidateL102(t, base))
	linux.LinuxProfile.RemoteProfile = "sandbox-host"
	if _, err := ValidateV3ProfileBindings(linux); !errors.Is(err, ErrInvalidLaneProfiles) {
		t.Fatalf("Linux config bound to another host profile error = %v", err)
	}
}

func TestLanesL102StrictYAMLShapeAndCredentialPathsAreNeverOpened(t *testing.T) {
	bindings := l102MacBindings(t, ReleaseLaneQAS)
	contents, err := yaml.Marshal(bindings)
	if err != nil {
		t.Fatal(err)
	}
	unknownField := strings.Replace(string(contents), "host: mac", "untrusted_path: /tmp/other-lane\nhost: mac", 1)
	if _, err := ParseV3ProfileBindings([]byte(unknownField)); !errors.Is(err, ErrInvalidLaneProfiles) {
		t.Fatalf("unknown YAML field error = %v", err)
	}
	aliased := strings.Replace(string(contents), "version: 3", "anchor: &lane dev\nversion: 3", 1)
	if _, err := ParseV3ProfileBindings([]byte(aliased)); !errors.Is(err, ErrInvalidLaneProfiles) {
		t.Fatalf("YAML anchor error = %v", err)
	}
	if _, err := ParseV3ProfileBindings(append(append([]byte(nil), contents...), []byte("---\nversion: 3\n")...)); !errors.Is(err, ErrInvalidLaneProfiles) {
		t.Fatalf("multi-document YAML error = %v", err)
	}

	mac := l102MacBindings(t, ReleaseLaneQAS)
	for _, profile := range mac.MacProfiles {
		for _, path := range []string{profile.Queued.KnownHostsFile, profile.Queued.DispatcherPrivateKeyFile, profile.Direct.ServerCAFile,
			profile.Direct.ClientCertificateFile, profile.Direct.ClientPrivateKeyFile} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("fixture credential path %q should remain absent (stat error %v)", path, err)
			}
		}
	}
	if _, err := ValidateV3ProfileBindings(mac); err != nil {
		t.Fatalf("path-only test identities were not accepted: %v", err)
	}
}

func TestLanesL102ProfileSchemaIsClosedAndRequiresLaneBinding(t *testing.T) {
	contents, err := os.ReadFile("schemas/v3/profiles.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required []string                   `json:"required"`
		Props    map[string]json.RawMessage `json:"properties"`
		Defs     map[string]json.RawMessage `json:"$defs"`
		Closed   *bool                      `json:"additionalProperties"`
	}
	if err := json.Unmarshal(contents, &schema); err != nil {
		t.Fatalf("decode profile schema: %v", err)
	}
	for _, key := range []string{"version", "release_lane", "host", "environments", "execution_contexts"} {
		if _, ok := schema.Props[key]; !ok {
			t.Fatalf("profile schema is missing %q", key)
		}
	}
	for _, key := range []string{"macProfile", "linuxProfile", "environment", "executionContext"} {
		if _, ok := schema.Defs[key]; !ok {
			t.Fatalf("profile schema is missing $defs.%s", key)
		}
	}
	if schema.Closed == nil || *schema.Closed || !reflect.DeepEqual(schema.Required, []string{"version", "release_lane", "host", "environments", "execution_contexts"}) {
		t.Fatalf("profile schema top-level required=%v additionalProperties=%v", schema.Required, schema.Closed)
	}
}

func l102MacBindings(t *testing.T, lane ReleaseLane) V3ProfileBindings {
	t.Helper()
	macNamespace, err := DeriveLaneNamespace(HostKindMac, lane, "")
	if err != nil {
		t.Fatal(err)
	}
	bindings := V3ProfileBindings{Version: VersionV3, ReleaseLane: lane, Host: HostKindMac}
	for index, profileName := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		namespace, err := DeriveLaneNamespace(HostKindLinux, lane, profileName)
		if err != nil {
			t.Fatal(err)
		}
		profile := laneRemoteProfiles[profileName]
		base := laneIndex(lane)*1000 + 1 + index*10
		controllerID := laneControllerID(lane, profileName)
		bindings.MacProfiles = append(bindings.MacProfiles, V3MacRemoteProfile{
			Profile: profileName,
			Queued: V3MacQueuedCredentials{
				Host: profile.publicIP, Port: namespace.QueuePort,
				KnownHostsFile:                 laneSecretPath(macNamespace.ServiceRoot, profileName, "ssh", "known_hosts"),
				HostKeyFingerprint:             l102Fingerprint(base),
				DispatcherPrivateKeyFile:       laneSecretPath(macNamespace.ServiceRoot, profileName, "ssh", "dispatcher_ed25519"),
				DispatcherPublicKeyFingerprint: l102Fingerprint(base + 1),
			},
			Direct: V3MacDirectCredentials{
				Name: namespace.DirectEndpointName, URL: namespace.DirectEndpointURL,
				ServerCAFile:                         laneSecretPath(macNamespace.ServiceRoot, profileName, "tls", "server-ca.pem"),
				ServerCAFingerprint:                  l102Fingerprint(base + 2),
				ClientCertificateFile:                laneSecretPath(macNamespace.ServiceRoot, profileName, "tls", "client.pem"),
				ClientCertificateFingerprint:         l102Fingerprint(base + 3),
				ClientPrivateKeyFile:                 laneSecretPath(macNamespace.ServiceRoot, profileName, "tls", "client.key"),
				ClientSPKIFingerprint:                l102Fingerprint(base + 4),
				ExpectedServerCertificateFingerprint: l102Fingerprint(base + 5),
				ExpectedClientCAFingerprint:          l102Fingerprint(base + 6),
				ClientURISAN:                         laneControllerURI(lane, profileName), ServerURISAN: laneServerURI(lane, profileName),
				ControllerType: string(domain.ControllerTypeDirectMTLS), ControllerID: controllerID,
			},
		})
		bindings.Environments = append(bindings.Environments, V3EnvironmentBinding{
			Name: profileName + "-dev", EffectiveAccount: LinuxAccount,
			AllowedTargets: []V3TargetBinding{{Kind: domain.TargetKindRemote, Profile: profileName}},
			AllowedControllers: []V3ControllerBinding{
				{Type: string(domain.ControllerTypeQueuedMac), ID: MacAccount},
				{Type: string(domain.ControllerTypeDirectMTLS), ID: controllerID},
			},
		})
		bindings.ExecutionContexts = append(bindings.ExecutionContexts, V3ExecutionContextBinding{
			Name: profileName + "-context", Environment: profileName + "-dev",
			Target: V3TargetBinding{Kind: domain.TargetKindRemote, Profile: profileName},
		})
	}
	if lane == ReleaseLanePRD {
		bindings.Environments = append(bindings.Environments, V3EnvironmentBinding{
			Name: "mac-dev", EffectiveAccount: MacAccount,
			AllowedTargets:     []V3TargetBinding{{Kind: domain.TargetKindLocal, Profile: "mac-workstation"}},
			AllowedControllers: []V3ControllerBinding{{Type: string(domain.ControllerTypeLocalUser), ID: MacAccount}},
		})
		bindings.ExecutionContexts = append(bindings.ExecutionContexts, V3ExecutionContextBinding{
			Name: "mac-local-context", Environment: "mac-dev",
			Target: V3TargetBinding{Kind: domain.TargetKindLocal, Profile: "mac-workstation"},
		})
	}
	return bindings
}

func l102LinuxBindings(t *testing.T, lane ReleaseLane, profileName string, mac ValidatedV3Profiles) V3ProfileBindings {
	t.Helper()
	namespace, err := DeriveLaneNamespace(HostKindLinux, lane, profileName)
	if err != nil {
		t.Fatal(err)
	}
	macProfile, ok := mac.MacProfile(profileName)
	if !ok {
		t.Fatalf("Mac test identity missing %s", profileName)
	}
	profile := laneRemoteProfiles[profileName]
	bindings := V3ProfileBindings{
		Version: VersionV3, ReleaseLane: lane, Host: HostKindLinux, RemoteProfile: profileName,
		LinuxProfile: &V3LinuxProfileBinding{
			RemoteProfile: profileName,
			Queued: V3LinuxQueuedCredentials{
				Host: profile.publicIP, Port: namespace.QueuePort,
				Bind:                     strings.TrimSuffix(namespace.DirectHTTPSBind, fmt.Sprintf(":%d", namespace.DirectPort)) + fmt.Sprintf(":%d", namespace.QueuePort),
				HostKeyFile:              filepath.Join(namespace.SSHAuthorityPath, "ssh_host_ed25519_key"),
				HostKeyFingerprint:       macProfile.Queued.HostKeyFingerprint,
				AuthorizedKeyFingerprint: macProfile.Queued.DispatcherPublicKeyFingerprint,
				Socket:                   namespace.PrivateSocket,
			},
			Direct: V3LinuxDirectCredentials{
				Name: namespace.DirectEndpointName, URL: namespace.DirectEndpointURL, Bind: namespace.DirectHTTPSBind,
				ServerCertificateFile:           laneSecretPath(namespace.ServiceRoot, profileName, "tls", "server.pem"),
				ServerCertificateFingerprint:    macProfile.Direct.ExpectedServerCertificateFingerprint,
				ServerPrivateKeyFile:            laneSecretPath(namespace.ServiceRoot, profileName, "tls", "server.key"),
				ServerSPKIFingerprint:           l102Fingerprint(80 + int(laneIndex(lane))*10 + profileIndex(profileName)),
				ClientCAFile:                    laneSecretPath(namespace.ServiceRoot, profileName, "tls", "client-ca.pem"),
				ClientCAFingerprint:             macProfile.Direct.ExpectedClientCAFingerprint,
				ClientPrincipalMapFile:          filepath.Join(namespace.ServiceRoot, "config", "client-principals.yaml"),
				AuthorizedClientFingerprint:     macProfile.Direct.ClientCertificateFingerprint,
				AuthorizedClientSPKIFingerprint: macProfile.Direct.ClientSPKIFingerprint,
				ClientURISAN:                    macProfile.Direct.ClientURISAN, ServerURISAN: macProfile.Direct.ServerURISAN,
				ControllerType: macProfile.Direct.ControllerType, ControllerID: macProfile.Direct.ControllerID,
			},
		},
		Environments: []V3EnvironmentBinding{{
			Name: profileName + "-dev", EffectiveAccount: LinuxAccount,
			AllowedTargets: []V3TargetBinding{{Kind: domain.TargetKindRemote, Profile: profileName}},
			AllowedControllers: []V3ControllerBinding{
				{Type: string(domain.ControllerTypeQueuedMac), ID: MacAccount},
				{Type: string(domain.ControllerTypeDirectMTLS), ID: laneControllerID(lane, profileName)},
			},
		}},
		ExecutionContexts: []V3ExecutionContextBinding{{
			Name: profileName + "-local-context", Environment: profileName + "-dev",
			Target: V3TargetBinding{Kind: domain.TargetKindRemote, Profile: profileName},
		}},
	}
	return bindings
}

func TestLanesL202LinuxHostKeyUsesRootOwnedSSHAuthority(t *testing.T) {
	for _, lane := range []ReleaseLane{ReleaseLaneDev, ReleaseLaneQAS} {
		mac, err := ValidateV3ProfileBindings(l102MacBindings(t, lane))
		if err != nil {
			t.Fatalf("validate Mac %s fixture: %v", lane, err)
		}
		for _, profile := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
			bindings := l102LinuxBindings(t, lane, profile, mac)
			namespace, err := DeriveLaneNamespace(HostKindLinux, lane, profile)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(namespace.SSHAuthorityPath, "ssh_host_ed25519_key")
			if bindings.LinuxProfile.Queued.HostKeyFile != want {
				t.Fatalf("%s/%s host key path=%q, want root-owned lane authority path %q", profile, lane, bindings.LinuxProfile.Queued.HostKeyFile, want)
			}
			if _, err := ValidateV3ProfileBindings(bindings); err != nil {
				t.Fatalf("validate %s/%s fixed host-key binding: %v", profile, lane, err)
			}
			bindings.LinuxProfile.Queued.HostKeyFile = laneSecretPath(namespace.ServiceRoot, profile, "ssh", "host_ed25519")
			if _, err := ValidateV3ProfileBindings(bindings); !errors.Is(err, ErrInvalidLaneProfiles) {
				t.Errorf("%s/%s user-owned service-root host-key binding error=%v, want %v", profile, lane, err, ErrInvalidLaneProfiles)
			}
		}
	}
}

func l102Fingerprint(number int) string { return fmt.Sprintf("sha256:%064x", number) }

func laneIndex(lane ReleaseLane) int {
	switch lane {
	case ReleaseLaneDev:
		return 1
	case ReleaseLaneQAS:
		return 2
	case ReleaseLanePRD:
		return 3
	default:
		return 0
	}
}

func profileIndex(profile string) int {
	switch profile {
	case "linux-host":
		return 1
	case "sandbox-host":
		return 2
	case "factory-sandbox":
		return 3
	default:
		return 0
	}
}

func mustValidateL102(t *testing.T, bindings V3ProfileBindings) ValidatedV3Profiles {
	t.Helper()
	validated, err := ValidateV3ProfileBindings(bindings)
	if err != nil {
		t.Fatal(err)
	}
	return validated
}

func filterContexts(contexts []V3ExecutionContextBinding, environment string) []V3ExecutionContextBinding {
	filtered := make([]V3ExecutionContextBinding, 0, len(contexts))
	for _, context := range contexts {
		if context.Environment != environment {
			filtered = append(filtered, context)
		}
	}
	return filtered
}

var _ = filepath.Join
