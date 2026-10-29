package config

import (
	"errors"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestSimpleLaneNamespaceUsesOnlyExistingHostResources(t *testing.T) {
	lanes := []struct {
		lane       ReleaseLane
		suffix     string
		directPort int
		runnerUnit string
	}{
		{ReleaseLanePRD, "", 8443, "runnerd.service"},
		{ReleaseLaneDev, "-dev", 8444, "runnerd-dev.service"},
		{ReleaseLaneQAS, "-qas", 8445, "runnerd-qas.service"},
	}
	profiles := []struct {
		name, publicIP, privateIP string
	}{
		{"linux-host", "129.151.232.40", "10.0.0.200"},
		{"sandbox-host", "132.226.205.205", "10.0.0.14"},
		{"factory-sandbox", "145.241.255.66", "10.0.0.30"},
	}
	for _, tc := range lanes {
		t.Run(string(tc.lane), func(t *testing.T) {
			mac, err := DeriveSimpleLaneNamespace(HostKindMac, tc.lane, "")
			if err != nil {
				t.Fatal(err)
			}
			wantMacRoot := filepath.Join("/Users", MacAccount, "Library", "Application Support", "RemoteSessionRunner"+tc.suffix)
			if mac.ServiceRoot != wantMacRoot || mac.LocalAgentLabel != "com.remote-session-runner.local"+tc.suffix ||
				mac.LocalDAgentLabel != "com.remote-session-runner.locald"+tc.suffix || mac.RegistryRoot != "" {
				t.Fatalf("Mac namespace=%+v", mac)
			}
			if tc.lane == ReleaseLanePRD && mac.ServiceRoot != MacServiceRoot {
				t.Fatalf("legacy PRD root changed: %q", mac.ServiceRoot)
			}
			for _, profile := range profiles {
				linux, err := DeriveSimpleLaneNamespace(HostKindLinux, tc.lane, profile.name)
				if err != nil {
					t.Fatal(err)
				}
				wantRoot := filepath.Join("/home", LinuxAccount, ".local", "share", "remote-session-runner"+tc.suffix)
				wantSSHAuthority := ""
				if tc.lane == ReleaseLanePRD {
					wantSSHAuthority = filepath.Join("/home", LinuxAccount, ".ssh", "authorized_keys")
				}
				if linux.ServiceRoot != wantRoot || linux.RunnerUnit != tc.runnerUnit || linux.DirectPort != tc.directPort ||
					linux.QueuePort != 22 || linux.QueuedSSHBind != "0.0.0.0:22" ||
					linux.QueuedSSHUnit != "" || linux.RunnerSlice != "" || linux.SSHAuthorityPath != wantSSHAuthority ||
					linux.SSHHostPublicKeyFile != "/etc/ssh/ssh_host_ed25519_key.pub" {
					t.Fatalf("%s namespace=%+v", profile.name, linux)
				}
				if linux.DirectEndpointURL != "https://"+profile.publicIP+":"+itoa(tc.directPort) ||
					linux.DirectHTTPSBind != profile.privateIP+":"+itoa(tc.directPort) {
					t.Fatalf("%s direct route=%+v", profile.name, linux)
				}
			}
		})
	}
}

func TestSimpleV3ProfilesBindExistingSSHAndPermitConfiguredMacLocal(t *testing.T) {
	for _, lane := range []ReleaseLane{ReleaseLaneDev, ReleaseLaneQAS} {
		bindings := simpleV3MacFixture(t, lane)
		validated, err := ValidateSimpleV3ProfileBindings(bindings)
		if err != nil {
			t.Fatalf("remote-only %s Mac config rejected: %v", lane, err)
		}
		if validated.Lane() != lane || len(validated.bindings.MacProfiles) != len(laneRemoteProfiles) {
			t.Fatalf("%s Mac profile identity or count=%s/%d", lane, validated.Lane(), len(validated.bindings.MacProfiles))
		}

		local := V3TargetBinding{Kind: domain.TargetKindLocal, Profile: "mac-workstation"}
		bindings.Environments = append(bindings.Environments, V3EnvironmentBinding{
			Name: "mac-local", EffectiveAccount: MacAccount,
			AllowedTargets:     []V3TargetBinding{local},
			AllowedControllers: []V3ControllerBinding{{Type: string(domain.ControllerTypeLocalUser), ID: MacAccount}},
		})
		bindings.ExecutionContexts = append(bindings.ExecutionContexts, V3ExecutionContextBinding{
			Name: "mac-local-context", Environment: "mac-local", Target: local,
		})
		if _, err := ValidateSimpleV3ProfileBindings(bindings); err != nil {
			t.Fatalf("configured Mac-local target rejected for %s: %v", lane, err)
		}
	}
}

func TestSimpleV3LinuxProfilePinsLaneRouteAndExistingHostKey(t *testing.T) {
	for _, lane := range []ReleaseLane{ReleaseLaneDev, ReleaseLaneQAS} {
		macBindings := simpleV3MacFixture(t, lane)
		mac, err := ValidateSimpleV3ProfileBindings(macBindings)
		if err != nil {
			t.Fatalf("Mac fixture %s: %v", lane, err)
		}
		for _, profile := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
			linuxBindings := l102LinuxBindings(t, lane, profile, mac)
			ns, err := DeriveSimpleLaneNamespace(HostKindLinux, lane, profile)
			if err != nil {
				t.Fatal(err)
			}
			linuxBindings.LinuxProfile.Queued.Port = 22
			linuxBindings.LinuxProfile.Queued.Bind = ns.QueuedSSHBind
			linuxBindings.LinuxProfile.Queued.HostKeyFile = ns.SSHHostPublicKeyFile
			linuxBindings.LinuxProfile.Direct.Name = ns.DirectEndpointName
			linuxBindings.LinuxProfile.Direct.URL = ns.DirectEndpointURL
			linuxBindings.LinuxProfile.Direct.Bind = ns.DirectHTTPSBind
			validated, err := ValidateSimpleV3ProfileBindings(linuxBindings)
			if err != nil {
				t.Fatalf("%s/%s simple Linux profile rejected: %v", profile, lane, err)
			}
			if validated.RemoteProfile() != profile {
				t.Fatalf("selected Linux profile=%q, want %q", validated.RemoteProfile(), profile)
			}

			for name, mutate := range map[string]func(*V3LinuxProfileBinding){
				"private SSH port": func(p *V3LinuxProfileBinding) { p.Queued.Port = 2224 },
				"new host authority": func(p *V3LinuxProfileBinding) {
					p.Queued.HostKeyFile = "/etc/remote-session-runner/ssh/dev/ssh_host_ed25519_key"
				},
				"foreign lane key": func(p *V3LinuxProfileBinding) {
					p.Direct.ServerPrivateKeyFile = filepath.Join(ns.ServiceRoot+"-qas", "secrets/profiles", profile, "tls/server.key")
				},
			} {
				t.Run(string(lane)+"/"+profile+"/"+name, func(t *testing.T) {
					candidate := cloneV3ProfileBindings(linuxBindings)
					mutate(candidate.LinuxProfile)
					if _, err := ValidateSimpleV3ProfileBindings(candidate); !errors.Is(err, ErrInvalidLaneProfiles) {
						t.Fatalf("unsafe binding accepted: %v", err)
					}
				})
			}
		}
	}
}

func simpleV3MacFixture(t *testing.T, lane ReleaseLane) V3ProfileBindings {
	t.Helper()
	bindings := l102MacBindings(t, lane)
	for i := range bindings.MacProfiles {
		bindings.MacProfiles[i].Queued.Port = 22
	}
	return bindings
}
