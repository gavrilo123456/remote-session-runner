package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
	"remote-session-runner/src/internal/domain"
)

func TestSimpleV3RuntimeSchemaRequiresClosedLaneConfiguration(t *testing.T) {
	contents, err := os.ReadFile("schemas/v3/runtime.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
		OneOf                []json.RawMessage          `json:"oneOf"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	if err := json.Unmarshal(contents, &schema); err != nil {
		t.Fatalf("decode V3 runtime schema: %v", err)
	}
	wantRequired := []string{"version", "release_lane", "environment_registry", "execution_contexts", "lane_profiles"}
	if !reflect.DeepEqual(schema.Required, wantRequired) || len(schema.OneOf) != 2 ||
		schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Fatalf("V3 runtime schema required=%v host variants=%d additionalProperties=%v", schema.Required, len(schema.OneOf), schema.AdditionalProperties)
	}
	for _, key := range []string{"mac", "linux", "mailboxes", "lane_profiles", "environment_registry", "execution_contexts"} {
		if _, ok := schema.Properties[key]; !ok {
			t.Fatalf("V3 runtime schema omits %q", key)
		}
	}
}

func TestSimpleV3RuntimeLoadsIndependentMacAndLinuxLaneProfiles(t *testing.T) {
	for _, lane := range []ReleaseLane{ReleaseLaneDev, ReleaseLaneQAS, ReleaseLanePRD} {
		t.Run("Mac/"+string(lane), func(t *testing.T) {
			bindings := simpleRuntimeMacBindings(t, lane)
			loaded := parseSimpleRuntimeFixture(t, simpleRuntimeDocument(t, bindings))
			macNS, _ := DeriveSimpleLaneNamespace(HostKindMac, lane, "")
			if loaded.Kind() != HostKindMac || loaded.ReleaseLane() != lane || loaded.SchemaVersion() != VersionV3 {
				t.Fatalf("loaded config identity = %s/%s/v%d", loaded.Kind(), loaded.ReleaseLane(), loaded.SchemaVersion())
			}
			settings, ok := loaded.MacSettings()
			if !ok || settings.ServiceRoot != macNS.ServiceRoot || settings.APISocket != macNS.APISocket || settings.LocalDSocket != macNS.LocalDSocket {
				t.Fatalf("Mac lane settings=%+v present=%t", settings, ok)
			}
			for _, profile := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
				host, ok := loaded.RemoteHost(profile)
				linuxNS, _ := DeriveSimpleLaneNamespace(HostKindLinux, lane, profile)
				if !ok || host.QueuedBridge == nil || host.QueuedBridge.Port != 22 || host.DirectEndpoint == nil ||
					host.DirectEndpoint.Endpoint != linuxNS.DirectEndpointURL ||
					host.DirectEndpoint.ClientURISAN != laneControllerURI(lane, profile) ||
					host.DirectEndpoint.ExpectedServerFingerprint == "" {
					t.Fatalf("%s route=%+v present=%t", profile, host, ok)
				}
			}
			mailbox, ok := loaded.Mailbox("default")
			if !ok || mailbox.Root != macNS.MailboxRoot || len(mailbox.AllowedExecution) != len(bindings.ExecutionContexts) {
				t.Fatalf("lane default mailbox=%+v present=%t", mailbox, ok)
			}
			if got := loaded.DefaultServiceLimits(); lane != ReleaseLanePRD &&
				(got.ActiveSessionsPerHost != 2 || got.RunningCommandsPerHost != 1) {
				t.Fatalf("%s default limits=%+v, want 2 sessions/1 command", lane, got)
			}
		})

		for _, profile := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
			t.Run(string(lane)+"/"+profile, func(t *testing.T) {
				mac, err := ValidateSimpleV3ProfileBindings(simpleRuntimeMacBindings(t, lane))
				if err != nil {
					t.Fatal(err)
				}
				bindings := simpleRuntimeLinuxBindings(t, lane, profile, mac)
				loaded := parseSimpleRuntimeFixture(t, simpleRuntimeDocument(t, bindings))
				linux, err := ValidateSimpleV3ProfileBindings(bindings)
				if err != nil {
					t.Fatal(err)
				}
				if err := ValidateV3MacLinuxProfilePair(mac, linux, profile); err != nil {
					t.Fatalf("paired Mac/Linux route rejected: %v", err)
				}
				ns, _ := DeriveSimpleLaneNamespace(HostKindLinux, lane, profile)
				settings, ok := loaded.LinuxSettings()
				if !ok || loaded.Kind() != HostKindLinux || loaded.ReleaseLane() != lane ||
					settings.ServiceRoot != ns.ServiceRoot || settings.DirectHTTPSBind != ns.DirectHTTPSBind ||
					settings.DirectPublicEndpoint != ns.DirectEndpointURL || settings.PrivateSocket != ns.PrivateSocket {
					t.Fatalf("%s/%s Linux settings=%+v present=%t", lane, profile, settings, ok)
				}
				key, ok := loaded.SecretReference(SecretLinuxServerTLSKey)
				if !ok || key.File != bindings.LinuxProfile.Direct.ServerPrivateKeyFile {
					t.Fatalf("Linux server key=%+v present=%t", key, ok)
				}
				if len(loaded.ExecutionContextNames()) != 1 {
					t.Fatalf("Linux contexts=%v, want exactly one local profile context", loaded.ExecutionContextNames())
				}
			})
		}
	}
}

func TestSimpleV3ReplacementRouteUsesPairedExplicitAddresses(t *testing.T) {
	for _, lane := range []ReleaseLane{ReleaseLaneDev, ReleaseLaneQAS} {
		t.Run(string(lane), func(t *testing.T) {
			macBindings := simpleRuntimeMacBindings(t, lane)
			ns, err := DeriveSimpleLaneNamespace(HostKindLinux, lane, "linux-host")
			if err != nil {
				t.Fatal(err)
			}
			for i := range macBindings.MacProfiles {
				if macBindings.MacProfiles[i].Profile == "linux-host" {
					macBindings.MacProfiles[i].Queued.Host = "79.72.94.59"
					macBindings.MacProfiles[i].Direct.URL = "https://79.72.94.59:" + itoa(ns.DirectPort)
				}
			}
			macProfiles, err := ValidateSimpleV3ProfileBindings(macBindings)
			if err != nil {
				t.Fatalf("replacement Mac route rejected: %v", err)
			}

			linuxBindings := simpleRuntimeLinuxBindings(t, lane, "linux-host", macProfiles)
			linuxBindings.LinuxProfile.Queued.Host = "79.72.94.59"
			linuxBindings.LinuxProfile.Direct.URL = "https://79.72.94.59:" + itoa(ns.DirectPort)
			linuxBindings.LinuxProfile.Direct.Bind = "10.0.0.228:" + itoa(ns.DirectPort)
			linuxProfiles, err := ValidateSimpleV3ProfileBindings(linuxBindings)
			if err != nil {
				t.Fatalf("replacement Linux route rejected: %v", err)
			}
			if err := ValidateV3MacLinuxProfilePair(macProfiles, linuxProfiles, "linux-host"); err != nil {
				t.Fatalf("matching replacement routes/pins rejected: %v", err)
			}
			loaded := parseSimpleRuntimeFixture(t, simpleRuntimeDocument(t, linuxBindings))
			settings, ok := loaded.LinuxSettings()
			if !ok || settings.DirectPublicEndpoint != "https://79.72.94.59:"+itoa(ns.DirectPort) ||
				settings.DirectHTTPSBind != "10.0.0.228:"+itoa(ns.DirectPort) {
				t.Fatalf("loaded replacement route=%+v present=%t", settings, ok)
			}

			mismatched := cloneV3ProfileBindings(linuxBindings)
			mismatched.LinuxProfile.Queued.Host = "129.151.232.40"
			mismatched.LinuxProfile.Direct.URL = "https://129.151.232.40:" + itoa(ns.DirectPort)
			mismatched.LinuxProfile.Direct.Bind = "10.0.0.200:" + itoa(ns.DirectPort)
			mismatchProfiles, err := ValidateSimpleV3ProfileBindings(mismatched)
			if err != nil {
				t.Fatalf("individually supported legacy route should remain structurally valid: %v", err)
			}
			if err := ValidateV3MacLinuxProfilePair(macProfiles, mismatchProfiles, "linux-host"); !errors.Is(err, ErrInvalidLaneProfiles) {
				t.Fatalf("mismatched Mac/worker URL accepted: %v", err)
			}

			invalid := cloneV3ProfileBindings(linuxBindings)
			invalid.LinuxProfile.Direct.Bind = "79.72.94.59:" + itoa(ns.DirectPort)
			if _, err := ValidateSimpleV3ProfileBindings(invalid); !errors.Is(err, ErrInvalidLaneProfiles) {
				t.Fatalf("non-private listener bind accepted: %v", err)
			}
			for name, endpoint := range map[string]string{
				"wrong lane port":       "https://79.72.94.59:" + itoa(ns.DirectPort+1),
				"empty query delimiter": "https://79.72.94.59:" + itoa(ns.DirectPort) + "?",
				"fragment delimiter":    "https://79.72.94.59:" + itoa(ns.DirectPort) + "#",
				"noncanonical address":  "https://079.72.94.59:" + itoa(ns.DirectPort),
			} {
				t.Run(name, func(t *testing.T) {
					bad := cloneV3ProfileBindings(linuxBindings)
					bad.LinuxProfile.Direct.URL = endpoint
					bad.LinuxProfile.Queued.Host = "79.72.94.59"
					if _, err := ValidateSimpleV3ProfileBindings(bad); !errors.Is(err, ErrInvalidLaneProfiles) {
						t.Fatalf("invalid direct endpoint %q accepted: %v", endpoint, err)
					}
				})
			}
		})
	}

	prd := simpleRuntimeMacBindings(t, ReleaseLanePRD)
	for i := range prd.MacProfiles {
		if prd.MacProfiles[i].Profile == "linux-host" {
			prd.MacProfiles[i].Queued.Host = "79.72.94.59"
			prd.MacProfiles[i].Direct.URL = "https://79.72.94.59:8443"
		}
	}
	if _, err := ValidateSimpleV3ProfileBindings(prd); !errors.Is(err, ErrInvalidLaneProfiles) {
		t.Fatalf("replacement route changed unsuffixed PRD: %v", err)
	}

	duplicate := simpleRuntimeMacBindings(t, ReleaseLaneDev)
	for i := range duplicate.MacProfiles {
		if duplicate.MacProfiles[i].Profile == "linux-host" || duplicate.MacProfiles[i].Profile == "sandbox-host" {
			duplicate.MacProfiles[i].Queued.Host = "79.72.94.59"
			duplicate.MacProfiles[i].Direct.URL = "https://79.72.94.59:8444"
		}
	}
	if _, err := ValidateSimpleV3ProfileBindings(duplicate); !errors.Is(err, ErrInvalidLaneProfiles) {
		t.Fatalf("duplicate worker routes were accepted: %v", err)
	}
}

func TestSimpleV3RuntimeResolvesExplicitOptionalMacLocalTarget(t *testing.T) {
	bindings := simpleRuntimeMacBindings(t, ReleaseLaneDev)
	local := V3TargetBinding{Kind: domain.TargetKindLocal, Profile: "mac-workstation"}
	bindings.Environments = append(bindings.Environments, V3EnvironmentBinding{
		Name: "mac-workstation-dev", EffectiveAccount: MacAccount,
		AllowedTargets:     []V3TargetBinding{local},
		AllowedControllers: []V3ControllerBinding{{Type: string(domain.ControllerTypeLocalUser), ID: MacAccount}},
	})
	bindings.ExecutionContexts = append(bindings.ExecutionContexts, V3ExecutionContextBinding{
		Name: "mac-workstation-context", Environment: "mac-workstation-dev", Target: local,
	})
	loaded := parseSimpleRuntimeFixture(t, simpleRuntimeDocument(t, bindings))
	context, ok := loaded.ExecutionContext("mac-workstation-context")
	if !ok || context.Target.Kind() != domain.TargetKindLocal || context.Target.Profile() != "mac-workstation" {
		t.Fatalf("explicit Mac-local context=%+v, present=%t", context, ok)
	}
	settings, ok := loaded.MacSettings()
	if !ok || settings.APISocket != filepath.Join(settings.ServiceRoot, "run", "local-api.sock") {
		t.Fatalf("Mac-local endpoint does not resolve within its lane: %+v", settings)
	}
}

func TestSimpleV3RuntimeRejectsCrossLaneAndUnsafeRuntimeBindings(t *testing.T) {
	mutations := map[string]func(*fileDocument){
		"foreign direct endpoint": func(document *fileDocument) {
			document.LaneProfiles.MacProfiles[0].Direct.URL = "https://129.151.232.40:8445"
		},
		"old dedicated ssh listener": func(document *fileDocument) {
			document.LaneProfiles.MacProfiles[0].Queued.Port = 2224
		},
		"foreign lane private key": func(document *fileDocument) {
			document.LaneProfiles.MacProfiles[0].Direct.ClientPrivateKeyFile = filepath.Join(MacServiceRoot+"-qas", "secrets/profiles/linux-host/tls/client.key")
		},
		"external dev mailbox": func(document *fileDocument) {
			document.Mailboxes["default"] = mailboxDocument{Root: filepath.Join(MacServiceRoot, "mailbox"), DefaultExecution: "linux-host-context", AllowedExecution: []string{"linux-host-context"}}
		},
		"missing release lane": func(document *fileDocument) { document.ReleaseLane = "" },
		"host route mismatch":  func(document *fileDocument) { document.LaneProfiles.Host = HostKindLinux },
		"global concurrency exceeds dev ceiling": func(document *fileDocument) {
			value := 3
			document.Limits.ActiveSessionsPerHost = &value
		},
		"environment concurrency exceeds dev ceiling": func(document *fileDocument) {
			value := 2
			environment := document.EnvironmentRegistry["linux-host-dev"]
			environment.ServiceLimits.RunningCommandsPerHost = &value
			document.EnvironmentRegistry["linux-host-dev"] = environment
		},
		"controller identity mismatch": func(document *fileDocument) {
			document.EnvironmentRegistry["linux-host-dev"] = environmentDocument{
				BaseSystem: "Ubuntu", HostClass: "Ubuntu host", EffectiveAccount: LinuxAccount,
				AllowedTargets:           []targetDocument{{Kind: domain.TargetKindRemote, Profile: "linux-host"}},
				AllowedSourceModes:       []domain.SourceMode{domain.SourceModeEmpty},
				AllowedRepositoryAliases: []string{},
				AllowedControllers:       []controllerDocument{{Type: domain.ControllerTypeDirectMTLS, ID: "other-controller"}},
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			document := simpleRuntimeDocument(t, simpleRuntimeMacBindings(t, ReleaseLaneDev))
			mutate(&document)
			contents, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parse(contents); err == nil {
				t.Fatal("unsafe or inconsistent V3 runtime config was accepted")
			}
		})
	}

	legacy := macConfigFixture
	if _, err := parse([]byte(legacy)); err != nil {
		t.Fatalf("existing unsuffixed V1 config compatibility failed: %v", err)
	}
	if got := loadFixture(t, legacy).ReleaseLane(); got != ReleaseLanePRD {
		t.Fatalf("legacy V1 release lane=%q, want unsuffixed PRD", got)
	}
	legacyV2WithLane := macV2ConfigFixture + "release_lane: dev\n"
	if _, err := parse([]byte(legacyV2WithLane)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("V2 accepted an ignored V3 lane declaration: %v", err)
	}
}

func parseSimpleRuntimeFixture(t *testing.T, document fileDocument) Config {
	t.Helper()
	contents, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := parse(contents)
	if err != nil {
		t.Fatalf("parse isolated V3 test-identity config: %v\n%s", err, contents)
	}
	return loaded
}

func simpleRuntimeMacBindings(t *testing.T, lane ReleaseLane) V3ProfileBindings {
	t.Helper()
	bindings := simpleV3MacFixture(t, lane)
	if lane == ReleaseLanePRD {
		bindings = l102MacBindings(t, lane)
		for i := range bindings.MacProfiles {
			bindings.MacProfiles[i].Queued.Port = 22
		}
	}
	return bindings
}

func simpleRuntimeLinuxBindings(t *testing.T, lane ReleaseLane, profile string, mac ValidatedV3Profiles) V3ProfileBindings {
	t.Helper()
	bindings := l102LinuxBindings(t, lane, profile, mac)
	ns, err := DeriveSimpleLaneNamespace(HostKindLinux, lane, profile)
	if err != nil {
		t.Fatal(err)
	}
	bindings.LinuxProfile.Queued.Port = 22
	bindings.LinuxProfile.Queued.Bind = ns.QueuedSSHBind
	bindings.LinuxProfile.Queued.HostKeyFile = ns.SSHHostPublicKeyFile
	bindings.LinuxProfile.Direct.Name = ns.DirectEndpointName
	bindings.LinuxProfile.Direct.URL = ns.DirectEndpointURL
	bindings.LinuxProfile.Direct.Bind = ns.DirectHTTPSBind
	return bindings
}

func simpleRuntimeDocument(t *testing.T, bindings V3ProfileBindings) fileDocument {
	t.Helper()
	document := fileDocument{
		Version: VersionV3, ReleaseLane: bindings.ReleaseLane,
		EnvironmentRegistry: make(map[string]environmentDocument),
		ExecutionContexts:   make(map[string]executionContextDocument),
		LaneProfiles: &laneProfilesDocument{
			Host: bindings.Host, RemoteProfile: bindings.RemoteProfile,
			MacProfiles:  append([]V3MacRemoteProfile(nil), bindings.MacProfiles...),
			LinuxProfile: bindings.LinuxProfile,
		},
	}
	for _, environment := range bindings.Environments {
		converted := environmentDocument{
			BaseSystem: "Ubuntu", HostClass: "Runner test host", EffectiveAccount: environment.EffectiveAccount,
			AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty}, AllowedRepositoryAliases: []string{},
		}
		if environment.EffectiveAccount == MacAccount {
			converted.BaseSystem = "macOS"
		}
		for _, target := range environment.AllowedTargets {
			converted.AllowedTargets = append(converted.AllowedTargets, targetDocument{Kind: target.Kind, Profile: target.Profile})
		}
		for _, controller := range environment.AllowedControllers {
			converted.AllowedControllers = append(converted.AllowedControllers, controllerDocument{Type: domain.ControllerType(controller.Type), ID: controller.ID})
		}
		document.EnvironmentRegistry[environment.Name] = converted
	}
	for _, context := range bindings.ExecutionContexts {
		document.ExecutionContexts[context.Name] = executionContextDocument{
			Environment:     context.Environment,
			ExecutionTarget: targetDocument{Kind: context.Target.Kind, Profile: context.Target.Profile},
		}
	}
	if bindings.Host == HostKindMac {
		ns, _ := DeriveSimpleLaneNamespace(HostKindMac, bindings.ReleaseLane, "")
		document.Mac = &macDocument{
			Account: MacAccount, ServiceRoot: ns.ServiceRoot, APISocket: ns.APISocket, LocalDSocket: ns.LocalDSocket,
			Database: ns.Database, MailboxRoot: ns.MailboxRoot, Workspaces: ns.Workspaces,
			ScriptTempRoot: ns.ScriptTempRoot, Backups: ns.Backups,
		}
		allowed := make([]string, 0, len(bindings.ExecutionContexts))
		for _, context := range bindings.ExecutionContexts {
			allowed = append(allowed, context.Name)
		}
		if len(allowed) == 0 {
			t.Fatal("test binding has no execution contexts")
		}
		document.Mailboxes = map[string]mailboxDocument{
			"default": {Root: ns.MailboxRoot, RepositoryAliases: []string{}, DefaultExecution: allowed[0], AllowedExecution: allowed},
		}
		return document
	}

	profile := bindings.LinuxProfile
	if profile == nil {
		t.Fatal("Linux test binding has no selected profile")
	}
	ns, _ := DeriveSimpleLaneNamespace(HostKindLinux, bindings.ReleaseLane, profile.RemoteProfile)
	document.Linux = &linuxDocument{
		Account: LinuxAccount, ServiceRoot: ns.ServiceRoot, Database: ns.Database, PrivateSocket: ns.PrivateSocket,
		Workspaces: ns.Workspaces, ScriptTempRoot: ns.ScriptTempRoot, Backups: ns.Backups,
		DirectHTTPSBind: profile.Direct.Bind, DirectPublicEndpoint: profile.Direct.URL,
		ServerCertificate: profile.Direct.ServerCertificateFile, ClientCA: profile.Direct.ClientCAFile,
		ClientPrincipalMap: profile.Direct.ClientPrincipalMapFile, TLSMinVersion: "1.3",
		RuntimeAdapter: "linux-host-process", RemoteTargetProfile: profile.RemoteProfile,
	}
	return document
}
