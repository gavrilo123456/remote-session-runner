package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

const macConfigFixture = `version: 1
mac:
  account: tomasz.walczuk
  service_root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner"
  api_socket: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/local-api.sock"
  locald_socket: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/locald.sock"
  sqlite: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/state/local.db"
  mailbox_root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox"
  workspaces: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/workspaces"
  script_temp_root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/tmp/scripts"
  backups: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups"
  remote_endpoint_profile: linux-poc
  remote_endpoint: https://129.151.232.40:8443
  remote_server_ca_certificate: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/poc-ca.pem"
  ssh_host_alias: remote-session-runner
  ssh_known_hosts: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/ssh_known_hosts"
  direct_client_certificate: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.pem"
  reconciliation_deadline: 24h
secret_references:
  dispatcher_ssh_key:
    file: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/dispatcher_ed25519"
  direct_client_private_key:
    file: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.key"
environment_registry:
  mac-dev:
    base_system: macOS
    host_class: macOS workstation
    effective_account: tomasz.walczuk
    allowed_targets:
      - kind: local
        profile: mac-workstation
    allowed_source_modes: [empty, local_worktree]
    allowed_repository_aliases: []
    allowed_controllers:
      - type: local_user
        id: tomasz.walczuk
  linux-dev:
    base_system: Ubuntu 20.04.6 LTS
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets:
      - kind: remote
        profile: linux-host
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers:
      - type: queued_mac
        id: tomasz.walczuk
      - type: direct_mtls
        id: tomasz.walczuk
`

const linuxConfigFixture = `version: 1
linux:
  account: ubuntu
  service_root: /home/ubuntu/.local/share/remote-session-runner
  sqlite: /home/ubuntu/.local/share/remote-session-runner/state/remote.db
  private_socket: /home/ubuntu/.local/share/remote-session-runner/run/runnerd.sock
  workspaces: /home/ubuntu/.local/share/remote-session-runner/workspaces
  script_temp_root: /home/ubuntu/.local/share/remote-session-runner/tmp/scripts
  backups: /home/ubuntu/.local/share/remote-session-runner/backups
  direct_https_bind: 10.0.0.200:8443
  direct_public_endpoint: https://129.151.232.40:8443
  server_cert: /home/ubuntu/.local/share/remote-session-runner/secrets/server.pem
  client_ca: /home/ubuntu/.local/share/remote-session-runner/secrets/client-ca.pem
  client_principal_map: /home/ubuntu/.local/share/remote-session-runner/config/client-principals.yaml
  tls_min_version: "1.3"
  runtime_adapter: linux-host-process
secret_references:
  linux_server_private_key:
    file: /home/ubuntu/.local/share/remote-session-runner/secrets/server.key
environment_registry:
  mac-dev:
    base_system: macOS
    host_class: macOS workstation
    effective_account: tomasz.walczuk
    allowed_targets:
      - kind: local
        profile: mac-workstation
    allowed_source_modes: [empty, local_worktree]
    allowed_repository_aliases: []
    allowed_controllers:
      - type: local_user
        id: tomasz.walczuk
  linux-dev:
    base_system: Ubuntu 20.04.6 LTS
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets:
      - kind: remote
        profile: linux-host
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers:
      - type: queued_mac
        id: tomasz.walczuk
      - type: direct_mtls
        id: tomasz.walczuk
`

func TestP010_D14LoadsSelectedHostConfigsAndDefaults(t *testing.T) {
	t.Run("Mac", func(t *testing.T) {
		config := loadFixture(t, macConfigFixture)
		if config.Kind() != HostKindMac {
			t.Fatalf("Kind() = %q, want %q", config.Kind(), HostKindMac)
		}
		settings, ok := config.MacSettings()
		if !ok || settings.Account != MacAccount || settings.ServiceRoot != MacServiceRoot || settings.RemoteEndpoint != PublicEndpoint ||
			settings.RemoteServerCA != filepath.Join(MacServiceRoot, "secrets/poc-ca.pem") {
			t.Fatalf("unexpected Mac settings: %+v, present=%v", settings, ok)
		}
		if settings.ReconciliationDeadline != DefaultReconciliationDeadline {
			t.Fatalf("reconciliation deadline = %s, want %s", settings.ReconciliationDeadline, DefaultReconciliationDeadline)
		}
		if _, ok := config.LinuxSettings(); ok {
			t.Fatal("Linux settings unexpectedly present in Mac config")
		}
		if got := config.DefaultSourceMode(); got != domain.SourceModeEmpty {
			t.Fatalf("DefaultSourceMode() = %q, want empty", got)
		}
		checkSelectedDefaults(t, config)
		key, ok := config.SecretReference(SecretDispatcherSSHKey)
		if !ok || key.File != filepath.Join(MacServiceRoot, "secrets/dispatcher_ed25519") {
			t.Fatalf("dispatcher key reference = %+v, present=%v", key, ok)
		}
		if _, ok := config.SecretReference(SecretDirectClientTLSKey); !ok {
			t.Fatal("direct client private-key reference is missing")
		}
	})

	t.Run("Linux", func(t *testing.T) {
		config := loadFixture(t, linuxConfigFixture)
		if config.Kind() != HostKindLinux {
			t.Fatalf("Kind() = %q, want %q", config.Kind(), HostKindLinux)
		}
		settings, ok := config.LinuxSettings()
		if !ok || settings.Account != LinuxAccount || settings.ServiceRoot != LinuxServiceRoot ||
			settings.DirectHTTPSBind != LinuxHTTPSBind || settings.DirectPublicEndpoint != PublicEndpoint || settings.TLSMinVersion != "1.3" {
			t.Fatalf("unexpected Linux settings: %+v, present=%v", settings, ok)
		}
		if _, ok := config.MacSettings(); ok {
			t.Fatal("Mac settings unexpectedly present in Linux config")
		}
		checkSelectedDefaults(t, config)
		key, ok := config.SecretReference(SecretLinuxServerTLSKey)
		if !ok || key.File != filepath.Join(LinuxServiceRoot, "secrets/server.key") {
			t.Fatalf("Linux server-key reference = %+v, present=%v", key, ok)
		}
	})
}

func TestP128HostProfileAndMTLSConfigFailuresAreClassifiableWithoutValues(t *testing.T) {
	invalidProfile := strings.Replace(linuxConfigFixture, "runtime_adapter: linux-host-process", "runtime_adapter: unknown-profile", 1)
	_, err := parse([]byte(invalidProfile))
	if !errors.Is(err, ErrInvalidConfig) || !errors.Is(err, ErrConfigHostProfile) || strings.Contains(err.Error(), "unknown-profile") {
		t.Fatalf("invalid host profile error=%v; want safe ErrConfigHostProfile classification", err)
	}
	missingKey := strings.Replace(linuxConfigFixture, "secret_references:\n  linux_server_private_key:\n    file: /home/ubuntu/.local/share/remote-session-runner/secrets/server.key\n", "secret_references: {}\n", 1)
	_, err = parse([]byte(missingKey))
	if !errors.Is(err, ErrInvalidSecretRef) {
		t.Fatalf("missing mTLS key reference error=%v, want ErrInvalidSecretRef", err)
	}
}

func TestP010_D14SelectedEnvironmentPoliciesAndLimits(t *testing.T) {
	config := loadFixture(t, macConfigFixture)
	if got, want := config.EnvironmentNames(), []string{"linux-dev", "mac-dev"}; !equalStrings(got, want) {
		t.Fatalf("EnvironmentNames() = %v, want %v", got, want)
	}

	mac, ok := config.Environment("mac-dev")
	if !ok || mac.BaseSystem() != "macOS" || mac.Policy().EffectiveAccount() != MacAccount {
		t.Fatalf("unexpected mac-dev environment: %+v, present=%v", mac, ok)
	}
	macTarget, _ := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	macControllerID, _ := domain.NewControllerID(MacAccount)
	macController, _ := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, macControllerID)
	localSource, _ := domain.NewLocalWorktreeSource("/tmp/allowed-worktree")
	if _, err := mac.Policy().ValidateSessionPolicy(domain.SessionPolicyRequest{
		Target: macTarget, Source: localSource, Controller: macController,
	}); err != nil {
		t.Fatalf("selected Mac local_worktree policy rejected: %v", err)
	}
	gitSource, _ := domain.NewGitRevisionSource("unconfigured", "HEAD")
	if _, err := mac.Policy().ValidateSessionPolicy(domain.SessionPolicyRequest{
		Target: macTarget, Source: gitSource, Controller: macController,
	}); !errors.Is(err, domain.ErrEnvironmentSourceMismatch) {
		t.Fatalf("Mac git_revision error = %v, want source mismatch", err)
	}

	linux, ok := config.Environment("linux-dev")
	if !ok || linux.BaseSystem() != "Ubuntu 20.04.6 LTS" || linux.Policy().EffectiveAccount() != LinuxAccount {
		t.Fatalf("unexpected linux-dev environment: %+v, present=%v", linux, ok)
	}
	linuxTarget, _ := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	for _, controllerType := range []domain.ControllerType{domain.ControllerTypeQueuedMac, domain.ControllerTypeDirectMTLS} {
		controllerID, _ := domain.NewControllerID(MacAccount)
		controller, _ := domain.NewControllerIdentity(controllerType, controllerID)
		if _, err := linux.Policy().ValidateSessionPolicy(domain.SessionPolicyRequest{
			Target: linuxTarget, Source: domain.NewEmptySource(), Controller: controller,
		}); err != nil {
			t.Fatalf("selected Linux controller %q rejected: %v", controllerType, err)
		}
		if _, err := linux.Policy().ValidateSessionPolicy(domain.SessionPolicyRequest{
			Target: linuxTarget, Source: localSource, Controller: controller,
		}); !errors.Is(err, domain.ErrEnvironmentSourceMismatch) {
			t.Fatalf("Linux local_worktree error = %v, want source mismatch", err)
		}
	}

	withOverrides := strings.Replace(macConfigFixture,
		"environment_registry:\n  mac-dev:",
		"limits:\n  active_sessions_per_host: 10\n  command_timeout: 15m\nenvironment_registry:\n  mac-dev:", 1)
	withOverrides = strings.Replace(withOverrides, "  linux-dev:\n", "  linux-dev:\n    service_limits:\n      active_sessions_per_host: 12\n", 1)
	config = loadFixture(t, withOverrides)
	if got := config.DefaultServiceLimits(); got.ActiveSessionsPerHost != 10 || got.CommandTimeout != 15*time.Minute {
		t.Fatalf("global overrides not applied: %+v", got)
	}
	linux, _ = config.Environment("linux-dev")
	mac, _ = config.Environment("mac-dev")
	if got := linux.Policy().ServiceLimits().ActiveSessionsPerHost; got != 12 {
		t.Fatalf("linux-dev active-session override = %d, want 12", got)
	}
	if got := mac.Policy().ServiceLimits().ActiveSessionsPerHost; got != 10 {
		t.Fatalf("mac-dev did not inherit host default: %d", got)
	}
}

func TestP010_D14RejectsUnknownInvalidAndInlineSecretConfig(t *testing.T) {
	privateMaterial := "-----BEGIN PRIVATE KEY----- test material -----END PRIVATE KEY-----"
	invalid := []struct {
		name string
		text string
	}{
		{"unknown security field", strings.Replace(linuxConfigFixture, "linux:\n", "linux:\n  allow_bearer: true\n", 1)},
		{"duplicate key", strings.Replace(linuxConfigFixture, "version: 1\n", "version: 1\nversion: 1\n", 1)},
		{"unsupported version", strings.Replace(linuxConfigFixture, "version: 1", "version: 3", 1)},
		{"both host sections", strings.Replace(linuxConfigFixture, "version: 1\n", "version: 1\nmac:\n  account: tomasz.walczuk\n", 1)},
		{"additional registry environment", strings.Replace(linuxConfigFixture, "  linux-dev:\n", "  staging: {}\n  linux-dev:\n", 1)},
		{"second document", linuxConfigFixture + "\n---\nversion: 1\n"},
		{"YAML alias", strings.Replace(linuxConfigFixture,
			"  base_system: Ubuntu 20.04.6 LTS\n    host_class: Ubuntu Linux host",
			"  base_system: &host Ubuntu 20.04.6 LTS\n    host_class: *host", 1)},
		{"custom YAML tag", strings.Replace(linuxConfigFixture, "  tls_min_version: \"1.3\"", "  tls_min_version: !secret \"1.3\"", 1)},
		{"TLS below mandatory minimum", strings.Replace(linuxConfigFixture, "  tls_min_version: \"1.3\"", "  tls_min_version: \"1.2\"", 1)},
		{"wrong selected service root", strings.Replace(linuxConfigFixture,
			"service_root: /home/ubuntu/.local/share/remote-session-runner",
			"service_root: /home/ubuntu/.local/share/remote-session-runner-other", 1)},
		{"wrong Mac service root", strings.Replace(macConfigFixture,
			"service_root: \"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner\"",
			"service_root: \"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner-other\"", 1)},
		{"wrong selected database path", strings.Replace(linuxConfigFixture,
			"/home/ubuntu/.local/share/remote-session-runner/state/remote.db",
			"/home/ubuntu/.local/share/remote-session-runner/state/other.db", 1)},
		{"wrong Mac server CA path", strings.Replace(macConfigFixture,
			"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/poc-ca.pem",
			"/tmp/untrusted-ca.pem", 1)},
		{"wrong public endpoint", strings.Replace(linuxConfigFixture, PublicEndpoint, "https://example.invalid:8443", 1)},
		{"wrong bind", strings.Replace(linuxConfigFixture, LinuxHTTPSBind, "0.0.0.0:8443", 1)},
		{"version-two Linux profile in version one", strings.Replace(linuxConfigFixture,
			"  runtime_adapter: linux-host-process\n", "  runtime_adapter: linux-host-process\n  remote_target_profile: linux-other\n", 1)},
		{"inline private key", strings.Replace(linuxConfigFixture,
			"/home/ubuntu/.local/share/remote-session-runner/secrets/server.key", privateMaterial, 1)},
		{"secret path escape", strings.Replace(linuxConfigFixture,
			"/home/ubuntu/.local/share/remote-session-runner/secrets/server.key",
			"/home/ubuntu/.local/share/remote-session-runner/secrets/../config/server.key", 1)},
		{"fixed request ceiling changed", strings.Replace(linuxConfigFixture,
			"environment_registry:\n", "limits:\n  serialized_request_bytes: 2048\nenvironment_registry:\n", 1)},
		{"fixed script ceiling changed", strings.Replace(linuxConfigFixture,
			"environment_registry:\n", "limits:\n  script_bytes_per_request: 1024\nenvironment_registry:\n", 1)},
		{"metadata retention below retry window", strings.Replace(linuxConfigFixture,
			"environment_registry:\n", "retention:\n  metadata_and_idempotency: 30d\nenvironment_registry:\n", 1)},
		{"nonpositive service limit", strings.Replace(linuxConfigFixture,
			"environment_registry:\n", "limits:\n  active_sessions_per_host: 0\nenvironment_registry:\n", 1)},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			_, err := parse([]byte(test.text))
			if err == nil {
				t.Fatal("parse accepted invalid configuration")
			}
			if strings.Contains(err.Error(), privateMaterial) {
				t.Fatal("parse error exposed inline private key material")
			}
		})
	}
	_, err := parse([]byte(strings.Replace(linuxConfigFixture,
		"/home/ubuntu/.local/share/remote-session-runner/secrets/server.key", privateMaterial, 1)))
	if errors.Is(err, ErrInvalidConfig) || !errors.Is(err, ErrInvalidSecretRef) {
		t.Fatalf("inline key error = %v, want invalid secret reference without parser detail", err)
	}
}

func TestP010_D14MacReconciliationDeadlineDefaultsWhenOmitted(t *testing.T) {
	withoutDeadline := strings.Replace(macConfigFixture, "  reconciliation_deadline: 24h\n", "", 1)
	config := loadFixture(t, withoutDeadline)
	settings, ok := config.MacSettings()
	if !ok || settings.ReconciliationDeadline != DefaultReconciliationDeadline {
		t.Fatalf("Mac reconciliation deadline = %s, present=%v; want default %s", settings.ReconciliationDeadline, ok, DefaultReconciliationDeadline)
	}
}

func TestP010_D14OwnerRestrictedConfigFile(t *testing.T) {
	path := writeConfigFile(t, linuxConfigFixture, 0o600)
	if _, err := LoadFile(path); err != nil {
		t.Fatalf("LoadFile(valid owner-only file) error = %v", err)
	}

	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); !errors.Is(err, ErrConfigPermissions) {
		t.Fatalf("LoadFile(group-readable file) error = %v, want ErrConfigPermissions", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(t.TempDir(), "config-link.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(link); !errors.Is(err, ErrConfigFile) {
		t.Fatalf("LoadFile(symlink) error = %v, want ErrConfigFile", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	wrongUID := uint32(os.Geteuid()) + 1
	if err := validateConfigFileOwner(info, wrongUID); !errors.Is(err, ErrConfigOwner) {
		t.Fatalf("validateConfigFileOwner(wrong owner) error = %v, want ErrConfigOwner", err)
	}

	tooLarge := writeConfigFile(t, strings.Repeat("x", MaxConfigBytes+1), 0o600)
	if _, err := LoadFile(tooLarge); !errors.Is(err, ErrConfigTooLarge) {
		t.Fatalf("LoadFile(oversized file) error = %v, want ErrConfigTooLarge", err)
	}
}

func TestP010_D14ConfigErrorsDoNotIncludeUnknownSecretFieldValue(t *testing.T) {
	secret := "never-log-this-private-value"
	text := strings.Replace(linuxConfigFixture, "linux:\n", "linux:\n  private_key: "+secret+"\n", 1)
	_, err := parse([]byte(text))
	if err == nil || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("parse(unknown private key field) error = %v, want ErrInvalidConfig", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("configuration error exposed the unknown private key field value")
	}
}

func checkSelectedDefaults(t *testing.T, config Config) {
	t.Helper()
	if got := config.DefaultSourceMode(); got != domain.SourceModeEmpty {
		t.Fatalf("DefaultSourceMode() = %q, want empty", got)
	}
	defaults := config.DefaultServiceLimits()
	want := domain.DefaultServiceLimits()
	if defaults != want {
		t.Fatalf("DefaultServiceLimits() = %+v, want %+v", defaults, want)
	}
	retention := config.Retention()
	if retention.MetadataAndIdempotency != 90*24*time.Hour || retention.OutputEvents != 30*24*time.Hour ||
		retention.MailboxACKGrace != 24*time.Hour || retention.MailboxUnacked != 7*24*time.Hour {
		t.Fatalf("Retention() = %+v, want selected defaults", retention)
	}
}

func loadFixture(t *testing.T, text string) Config {
	t.Helper()
	config, err := LoadFile(writeConfigFile(t, text, 0o600))
	if err != nil {
		t.Fatalf("LoadFile(fixture) error = %v", err)
	}
	return config
}

func writeConfigFile(t *testing.T, text string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

const macV2ConfigFixture = `version: 2
mac:
  account: tomasz.walczuk
  service_root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner"
  api_socket: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/local-api.sock"
  locald_socket: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/locald.sock"
  sqlite: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/state/local.db"
  workspaces: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/workspaces"
  script_temp_root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/tmp/scripts"
  backups: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups"
  reconciliation_deadline: 24h
environment_registry:
  mac-dev:
    base_system: macOS
    host_class: macOS workstation
    effective_account: tomasz.walczuk
    allowed_targets: [{kind: local, profile: mac-workstation}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers: [{type: local_user, id: tomasz.walczuk}]
  linux-dev:
    base_system: Ubuntu 20.04.6 LTS
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets: [{kind: remote, profile: linux-host}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers:
      - {type: queued_mac, id: tomasz.walczuk}
      - {type: direct_mtls, id: tomasz.walczuk}
  linux-build-dev:
    base_system: Ubuntu 22.04 LTS
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets: [{kind: remote, profile: linux-build-host}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers:
      - {type: queued_mac, id: tomasz.walczuk}
      - {type: direct_mtls, id: tomasz.walczuk}
execution_contexts:
  mac-local:
    environment: mac-dev
    execution_target: {kind: local, profile: mac-workstation}
  ubuntu-current:
    environment: linux-dev
    execution_target: {kind: remote, profile: linux-host}
  ubuntu-build:
    environment: linux-build-dev
    execution_target: {kind: remote, profile: linux-build-host}
remote_hosts:
  linux-host:
    account: ubuntu
    queued_bridge:
      host: 129.151.232.40
      port: 22
      known_hosts: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/ssh_known_hosts"
      private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/dispatcher_ed25519"
    direct_endpoint:
      name: linux-poc
      url: https://129.151.232.40:8443
      server_ca: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/poc-ca.pem"
      client_certificate: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.pem"
      client_private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-client.key"
  linux-build-host:
    account: ubuntu
    queued_bridge:
      host: 203.0.113.20
      port: 22
      known_hosts: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/linux-build_known_hosts"
      private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/linux-build_dispatcher_ed25519"
    direct_endpoint:
      name: linux-build-poc
      url: https://203.0.113.20:8443
      server_ca: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/linux-build-ca.pem"
      client_certificate: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/linux-build-client.pem"
      client_private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/linux-build-client.key"
mailboxes:
  default:
    root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox"
    repository_aliases: [remote-session-runner]
    default_execution: mac-local
    allowed_execution: [mac-local, ubuntu-current]
  analytics:
    root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailboxes/analytics"
    repository_aliases: [analytics-dbt]
    default_execution: ubuntu-current
    allowed_execution: [ubuntu-current, ubuntu-build]
`

const linuxV2ConfigFixture = `version: 2
linux:
  account: ubuntu
  service_root: /home/ubuntu/.local/share/remote-session-runner
  sqlite: /home/ubuntu/.local/share/remote-session-runner/state/remote.db
  private_socket: /home/ubuntu/.local/share/remote-session-runner/run/runnerd.sock
  workspaces: /home/ubuntu/.local/share/remote-session-runner/workspaces
  script_temp_root: /home/ubuntu/.local/share/remote-session-runner/tmp/scripts
  backups: /home/ubuntu/.local/share/remote-session-runner/backups
  direct_https_bind: 10.0.0.201:8443
  direct_public_endpoint: https://198.51.100.20:8443
  server_cert: /home/ubuntu/.local/share/remote-session-runner/secrets/server.pem
  client_ca: /home/ubuntu/.local/share/remote-session-runner/secrets/client-ca.pem
  client_principal_map: /home/ubuntu/.local/share/remote-session-runner/config/client-principals.yaml
  tls_min_version: "1.3"
  runtime_adapter: linux-host-process
  remote_target_profile: linux-build-host
secret_references:
  linux_server_private_key:
    file: /home/ubuntu/.local/share/remote-session-runner/secrets/server.key
environment_registry:
  linux-build-dev:
    base_system: Ubuntu 22.04 LTS
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets: [{kind: remote, profile: linux-build-host}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers:
      - {type: queued_mac, id: tomasz.walczuk}
      - {type: direct_mtls, id: tomasz.walczuk}
`

func TestP150V1SynthesizesTheLegacyDefaultRegistry(t *testing.T) {
	loaded := loadFixture(t, macConfigFixture)
	if loaded.SchemaVersion() != VersionV1 {
		t.Fatalf("SchemaVersion() = %d, want %d", loaded.SchemaVersion(), VersionV1)
	}
	if got, want := loaded.ExecutionContextNames(), []string{"mac-local", "ubuntu-current"}; !equalStrings(got, want) {
		t.Fatalf("ExecutionContextNames() = %v, want %v", got, want)
	}
	if got, want := loaded.RemoteHostNames(), []string{"linux-host"}; !equalStrings(got, want) {
		t.Fatalf("RemoteHostNames() = %v, want %v", got, want)
	}
	if got, want := loaded.DirectEndpointNames(), []string{"linux-poc"}; !equalStrings(got, want) {
		t.Fatalf("DirectEndpointNames() = %v, want %v", got, want)
	}
	mailbox, ok := loaded.Mailbox("default")
	if !ok || mailbox.Root != filepath.Join(MacServiceRoot, "mailbox") || mailbox.DefaultExecution != "mac-local" ||
		!equalStrings(mailbox.AllowedExecution, []string{"mac-local", "ubuntu-current"}) {
		t.Fatalf("legacy default mailbox = %+v, present=%v", mailbox, ok)
	}
	remoteTarget, _ := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	context, ok := loaded.ExecutionContextFor("linux-dev", remoteTarget)
	if !ok || context.Name != "ubuntu-current" {
		t.Fatalf("legacy remote context = %+v, present=%v", context, ok)
	}
	host, ok := loaded.RemoteHost("linux-host")
	if !ok || host.Account != LinuxAccount || host.QueuedBridge == nil || host.QueuedBridge.Host != "129.151.232.40" ||
		host.DirectEndpoint == nil || host.DirectEndpoint.Name != "linux-poc" {
		t.Fatalf("legacy remote host = %+v, present=%v", host, ok)
	}
}

func TestP150LoadsValidatedNamedMacRegistries(t *testing.T) {
	loaded := loadFixture(t, macV2ConfigFixture)
	if loaded.SchemaVersion() != VersionV2 || loaded.Kind() != HostKindMac {
		t.Fatalf("v2 config identity = version %d kind %q", loaded.SchemaVersion(), loaded.Kind())
	}
	if got, want := loaded.ExecutionContextNames(), []string{"mac-local", "ubuntu-build", "ubuntu-current"}; !equalStrings(got, want) {
		t.Fatalf("ExecutionContextNames() = %v, want %v", got, want)
	}
	if got, want := loaded.RemoteHostNames(), []string{"linux-build-host", "linux-host"}; !equalStrings(got, want) {
		t.Fatalf("RemoteHostNames() = %v, want %v", got, want)
	}
	if got, want := loaded.DirectEndpointNames(), []string{"linux-build-poc", "linux-poc"}; !equalStrings(got, want) {
		t.Fatalf("DirectEndpointNames() = %v, want %v", got, want)
	}
	if got, want := loaded.MailboxNames(), []string{"analytics", "default"}; !equalStrings(got, want) {
		t.Fatalf("MailboxNames() = %v, want %v", got, want)
	}

	endpoint, ok := loaded.DirectEndpoint("linux-build-poc")
	if !ok || endpoint.TargetProfile != "linux-build-host" || endpoint.Endpoint != "https://203.0.113.20:8443" {
		t.Fatalf("direct endpoint binding = %+v, present=%v", endpoint, ok)
	}
	remoteTarget, _ := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-build-host")
	context, ok := loaded.ExecutionContextFor("linux-build-dev", remoteTarget)
	if !ok || context.Name != "ubuntu-build" {
		t.Fatalf("remote context lookup = %+v, present=%v", context, ok)
	}

	mailbox, ok := loaded.Mailbox("analytics")
	if !ok || mailbox.DefaultExecution != "ubuntu-current" || !equalStrings(mailbox.RepositoryAliases, []string{"analytics-dbt"}) {
		t.Fatalf("analytics mailbox = %+v, present=%v", mailbox, ok)
	}
	mailbox.RepositoryAliases[0] = "changed"
	mailbox.AllowedExecution[0] = "changed"
	again, _ := loaded.Mailbox("analytics")
	if !equalStrings(again.RepositoryAliases, []string{"analytics-dbt"}) || !equalStrings(again.AllowedExecution, []string{"ubuntu-current", "ubuntu-build"}) {
		t.Fatalf("Mailbox() returned mutable slices: %+v", again)
	}

	host, ok := loaded.RemoteHost("linux-host")
	if !ok || host.QueuedBridge == nil {
		t.Fatalf("linux host = %+v, present=%v", host, ok)
	}
	host.QueuedBridge.Host = "changed"
	host.DirectEndpoint = nil
	againHost, _ := loaded.RemoteHost("linux-host")
	if againHost.QueuedBridge == nil || againHost.QueuedBridge.Host != "129.151.232.40" || againHost.DirectEndpoint == nil {
		t.Fatalf("RemoteHost() returned mutable pointers: %+v", againHost)
	}

	settings, ok := loaded.MacSettings()
	if !ok || settings.MailboxRoot != "" || settings.RemoteEndpointProfile != "" {
		t.Fatalf("v2 unexpectedly exposed a singleton route: %+v", settings)
	}
}

func TestP158V2AllowsOnlyCleanExternalNonDefaultMailboxRoots(t *testing.T) {
	const externalRoot = "/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-"
	loaded := loadFixture(t, p158ConfigWithExternalMailbox(externalRoot))
	mailbox, ok := loaded.Mailbox("slidestud-io")
	if !ok || mailbox.Root != externalRoot || mailbox.DefaultExecution != "ubuntu-current" ||
		!equalStrings(mailbox.RepositoryAliases, []string{"slidestud-io"}) ||
		!equalStrings(mailbox.AllowedExecution, []string{"ubuntu-current"}) {
		t.Fatalf("external mailbox = %+v, present=%v", mailbox, ok)
	}

	for _, path := range []string{
		externalRoot,
		"/Volumes/controlled-volume/any-location/mailbox-for-builds",
	} {
		if !validMailboxRoot(MacServiceRoot, "slidestud-io", path) {
			t.Fatalf("validMailboxRoot rejected clean external root %q", path)
		}
	}
	for _, path := range []string{
		"relative/mailbox", "/tmp/../safe-mailbox", string(filepath.Separator), MacServiceRoot,
		filepath.Join(MacServiceRoot, "unapproved-mailbox"),
	} {
		if validMailboxRoot(MacServiceRoot, "slidestud-io", path) {
			t.Fatalf("validMailboxRoot accepted unsafe or ambiguous root %q", path)
		}
	}
	if validMailboxRoot(MacServiceRoot, "default", externalRoot) {
		t.Fatal("default mailbox was allowed to move outside its compatibility root")
	}

	for _, root := range []string{
		"relative/mailbox", "/tmp/../safe-mailbox", string(filepath.Separator),
		filepath.Join(MacServiceRoot, "unapproved-mailbox"),
	} {
		if _, err := parse([]byte(p158ConfigWithExternalMailbox(root))); err == nil {
			t.Fatalf("parse accepted invalid external root %q", root)
		}
	}
	defaultExternal := strings.Replace(macV2ConfigFixture,
		`root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox"`,
		`root: "/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-"`, 1)
	if _, err := parse([]byte(defaultExternal)); err == nil {
		t.Fatal("parse allowed the default mailbox to move outside its compatibility root")
	}
	nested := p158ConfigWithExternalMailbox(externalRoot) + `  nested:
    root: "/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-/nested"
    repository_aliases: [nested]
    default_execution: ubuntu-current
    allowed_execution: [ubuntu-current]
`
	if _, err := parse([]byte(nested)); err == nil {
		t.Fatal("parse accepted nested external mailbox roots")
	}
	caseAlias := p158ConfigWithExternalMailbox(externalRoot) + `  alias:
    root: "/Users/tomasz.walczuk/projects/SlideStud.IO/tmp/mailbox-"
    repository_aliases: [alias]
    default_execution: ubuntu-current
    allowed_execution: [ubuntu-current]
`
	if _, err := parse([]byte(caseAlias)); err == nil {
		t.Fatal("parse accepted a case-normalized alias of an external mailbox root")
	}
	serviceRootAlias := p158ConfigWithExternalMailbox(filepath.Join(strings.ToLower(MacServiceRoot), "mailbox-alias"))
	if _, err := parse([]byte(serviceRootAlias)); err == nil {
		t.Fatal("parse accepted a case-normalized alias below the service root")
	}
	unicodeAlias := p158ConfigWithExternalMailbox("/Volumes/controlled/Café/mailbox") + `  unicode-alias:
    root: "/Volumes/controlled/Café/mailbox"
    repository_aliases: [unicode-alias]
    default_execution: ubuntu-current
    allowed_execution: [ubuntu-current]
`
	if _, err := parse([]byte(unicodeAlias)); err == nil {
		t.Fatal("parse accepted a Unicode-normalized alias of an external mailbox root")
	}
}

func p158ConfigWithExternalMailbox(root string) string {
	return macV2ConfigFixture + `  slidestud-io:
    root: "` + root + `"
    repository_aliases: [slidestud-io]
    default_execution: ubuntu-current
    allowed_execution: [ubuntu-current]
`
}

func TestP150LoadsGenericLinuxV2HostProfile(t *testing.T) {
	loaded := loadFixture(t, linuxV2ConfigFixture)
	if loaded.SchemaVersion() != VersionV2 || loaded.Kind() != HostKindLinux {
		t.Fatalf("v2 Linux identity = version %d kind %q", loaded.SchemaVersion(), loaded.Kind())
	}
	settings, ok := loaded.LinuxSettings()
	if !ok || settings.DirectHTTPSBind != "10.0.0.201:8443" || settings.DirectPublicEndpoint != "https://198.51.100.20:8443" {
		t.Fatalf("v2 Linux settings = %+v, present=%v", settings, ok)
	}
	if got, want := loaded.EnvironmentNames(), []string{"linux-build-dev"}; !equalStrings(got, want) {
		t.Fatalf("EnvironmentNames() = %v, want %v", got, want)
	}
}

func TestP150AllowsDirectOnlyProfilesOutsideMailboxRoutes(t *testing.T) {
	directOnly := strings.Replace(macV2ConfigFixture,
		"    queued_bridge:\n      host: 203.0.113.20\n      port: 22\n      known_hosts: \"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/linux-build_known_hosts\"\n      private_key: \"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/linux-build_dispatcher_ed25519\"\n", "", 1)
	directOnly = strings.Replace(directOnly, "allowed_execution: [ubuntu-current, ubuntu-build]", "allowed_execution: [ubuntu-current]", 1)
	loaded := loadFixture(t, directOnly)
	host, ok := loaded.RemoteHost("linux-build-host")
	if !ok || host.QueuedBridge != nil || host.DirectEndpoint == nil || host.DirectEndpoint.Name != "linux-build-poc" {
		t.Fatalf("direct-only profile = %+v, present=%v", host, ok)
	}
}

func TestP150AllowsQueuedOnlyProfilesForMailboxRoutes(t *testing.T) {
	queuedOnly := strings.Replace(macV2ConfigFixture,
		"    direct_endpoint:\n      name: linux-build-poc\n      url: https://203.0.113.20:8443\n      server_ca: \"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/linux-build-ca.pem\"\n      client_certificate: \"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/linux-build-client.pem\"\n      client_private_key: \"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/linux-build-client.key\"\n", "", 1)
	loaded := loadFixture(t, queuedOnly)
	host, ok := loaded.RemoteHost("linux-build-host")
	if !ok || host.QueuedBridge == nil || host.DirectEndpoint != nil {
		t.Fatalf("queued-only profile = %+v, present=%v", host, ok)
	}
	if _, ok := loaded.DirectEndpoint("linux-build-poc"); ok {
		t.Fatal("queued-only profile retained a direct endpoint")
	}
}

func TestP150RejectsUnsafeOrAmbiguousV2Registries(t *testing.T) {
	invalid := []struct {
		name string
		text string
	}{
		{"legacy fields in v2 Mac", strings.Replace(macV2ConfigFixture, "  reconciliation_deadline: 24h\n", "  reconciliation_deadline: 24h\n  mailbox_root: \"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox\"\n", 1)},
		{"legacy secret references in v2 Mac", strings.Replace(macV2ConfigFixture, "environment_registry:\n", "secret_references:\n  dispatcher_ssh_key:\n    file: \"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/dispatcher_ed25519\"\nenvironment_registry:\n", 1)},
		{"unsafe queued private key", strings.Replace(macV2ConfigFixture, "secrets/dispatcher_ed25519", "secrets/../dispatcher_ed25519", 1)},
		{"unsafe direct certificate", strings.Replace(macV2ConfigFixture, "secrets/poc-ca.pem", "/tmp/poc-ca.pem", 1)},
		{"invalid direct endpoint", strings.Replace(macV2ConfigFixture, "https://129.151.232.40:8443", "http://129.151.232.40:8443", 1)},
		{"direct endpoint wrong port", strings.Replace(macV2ConfigFixture, "https://129.151.232.40:8443", "https://129.151.232.40:9443", 1)},
		{"direct endpoint contains path", strings.Replace(macV2ConfigFixture, "https://129.151.232.40:8443", "https://129.151.232.40:8443/not-allowed", 1)},
		{"direct endpoint contains user info", strings.Replace(macV2ConfigFixture, "https://129.151.232.40:8443", "https://untrusted@129.151.232.40:8443", 1)},
		{"Linux v2 TLS below mandatory minimum", strings.Replace(linuxV2ConfigFixture, "tls_min_version: \"1.3\"", "tls_min_version: \"1.2\"", 1)},
		{"duplicate direct endpoint name", strings.Replace(macV2ConfigFixture, "name: linux-build-poc", "name: linux-poc", 1)},
		{"duplicate execution context pair", strings.Replace(macV2ConfigFixture, "remote_hosts:\n", "  duplicate-ubuntu-current:\n    environment: linux-dev\n    execution_target: {kind: remote, profile: linux-host}\nremote_hosts:\n", 1)},
		{"duplicate mailbox root", strings.Replace(macV2ConfigFixture, "mailboxes/analytics", "mailbox", 1)},
		{"unknown default context", strings.Replace(macV2ConfigFixture, "default_execution: ubuntu-current", "default_execution: not-configured", 1)},
		{"default missing from allow list", strings.Replace(macV2ConfigFixture, "allowed_execution: [ubuntu-current, ubuntu-build]", "allowed_execution: [ubuntu-build]", 1)},
		{"unknown remote profile", strings.Replace(macV2ConfigFixture, "  ubuntu-build:\n    environment: linux-build-dev\n    execution_target: {kind: remote, profile: linux-build-host}", "  ubuntu-build:\n    environment: linux-build-dev\n    execution_target: {kind: remote, profile: missing-host}", 1)},
		{"invalid local profile", strings.Replace(macV2ConfigFixture, "  mac-local:\n    environment: mac-dev\n    execution_target: {kind: local, profile: mac-workstation}", "  mac-local:\n    environment: mac-dev\n    execution_target: {kind: local, profile: another-mac}", 1)},
		{"local effective account mismatch", strings.Replace(macV2ConfigFixture, "  mac-dev:\n    base_system: macOS\n    host_class: macOS workstation\n    effective_account: tomasz.walczuk", "  mac-dev:\n    base_system: macOS\n    host_class: macOS workstation\n    effective_account: ubuntu", 1)},
		{"remote effective account mismatch", strings.Replace(macV2ConfigFixture, "  linux-dev:\n    base_system: Ubuntu 20.04.6 LTS\n    host_class: Ubuntu Linux host\n    effective_account: ubuntu", "  linux-dev:\n    base_system: Ubuntu 20.04.6 LTS\n    host_class: Ubuntu Linux host\n    effective_account: tomasz.walczuk", 1)},
		{"remote account is fixed", strings.Replace(macV2ConfigFixture, "  linux-build-host:\n    account: ubuntu", "  linux-build-host:\n    account: other", 1)},
		{"remote mailbox context needs bridge", strings.Replace(macV2ConfigFixture, "    queued_bridge:\n      host: 203.0.113.20\n      port: 22\n      known_hosts: \"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/linux-build_known_hosts\"\n      private_key: \"/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/linux-build_dispatcher_ed25519\"\n", "", 1)},
		{"Linux v2 has Mac registries", linuxV2ConfigFixture + "mailboxes: {}\n"},
		{"Linux v2 profile mismatch", strings.Replace(linuxV2ConfigFixture, "profile: linux-build-host", "profile: other-host", 1)},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parse([]byte(test.text)); err == nil {
				t.Fatal("parse accepted unsafe or ambiguous v2 registry")
			}
		})
	}

	invalidHostProfile := strings.Replace(linuxV2ConfigFixture, "runtime_adapter: linux-host-process", "runtime_adapter: unknown-profile", 1)
	_, err := parse([]byte(invalidHostProfile))
	if !errors.Is(err, ErrInvalidConfig) || !errors.Is(err, ErrConfigHostProfile) || strings.Contains(err.Error(), "unknown-profile") {
		t.Fatalf("v2 invalid host profile error=%v; want safe ErrConfigHostProfile classification", err)
	}
}
