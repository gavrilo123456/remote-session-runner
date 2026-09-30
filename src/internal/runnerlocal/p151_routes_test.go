package runnerlocal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/opshealth"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
)

func TestP151V1SynthesizedRouteBuildsPinnedSSHCaller(t *testing.T) {
	loaded := p151LoadOwnerOnlyConfig(t, p151V1MacConfig)
	caller := &p151RouteCaller{name: "legacy"}
	var configured []sshclient.Config

	resolver, err := newPinnedRemoteCallerResolverWithFactory(loaded, func(settings sshclient.Config) (dispatcher.RemoteCaller, error) {
		configured = append(configured, settings)
		return caller, nil
	})
	if err != nil {
		t.Fatalf("newPinnedRemoteCallerResolverWithFactory(v1) error = %v", err)
	}
	if got, want := resolver.RemoteCallerProfiles(), []string{"linux-host"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("v1 queued profiles = %v, want %v", got, want)
	}
	if len(configured) != 1 {
		t.Fatalf("v1 factory calls = %d, want 1", len(configured))
	}
	want := sshclient.Config{
		User:           config.LinuxAccount,
		Host:           "129.151.232.40",
		Port:           22,
		KnownHostsFile: filepath.Join(config.MacServiceRoot, "secrets", "ssh_known_hosts"),
		IdentityFile:   filepath.Join(config.MacServiceRoot, "secrets", "dispatcher_ed25519"),
	}
	if got := configured[0]; got != want {
		t.Fatalf("v1 pinned SSH config = %+v, want %+v", got, want)
	}
	resolved, err := resolver.ResolveRemoteCaller("linux-host")
	if err != nil || resolved != caller {
		t.Fatalf("v1 resolver returned caller=%v err=%v, want configured legacy caller", resolved, err)
	}
}

func TestP151V2BuildsOnlyQueuedPinnedSSHCallers(t *testing.T) {
	loaded := p151LoadOwnerOnlyConfig(t, p151V2MacConfig)
	callers := map[string]*p151RouteCaller{
		"198.51.100.11": {name: "queued-a"},
		"198.51.100.12": {name: "queued-b"},
	}
	var configured []sshclient.Config

	resolver, err := newPinnedRemoteCallerResolverWithFactory(loaded, func(settings sshclient.Config) (dispatcher.RemoteCaller, error) {
		configured = append(configured, settings)
		caller, ok := callers[settings.Host]
		if !ok {
			return nil, errors.New("unexpected queued bridge host")
		}
		return caller, nil
	})
	if err != nil {
		t.Fatalf("newPinnedRemoteCallerResolverWithFactory(v2) error = %v", err)
	}
	if got, want := resolver.RemoteCallerProfiles(), []string{"queued-a", "queued-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("v2 queued profiles = %v, want %v", got, want)
	}
	if len(configured) != 2 {
		t.Fatalf("v2 factory calls = %d, want 2; direct-only profiles must not create SSH callers", len(configured))
	}

	expected := map[string]sshclient.Config{
		"198.51.100.11": {
			User:           config.LinuxAccount,
			Host:           "198.51.100.11",
			Port:           2201,
			KnownHostsFile: filepath.Join(config.MacServiceRoot, "secrets", "queued-a_known_hosts"),
			IdentityFile:   filepath.Join(config.MacServiceRoot, "secrets", "queued-a_ed25519"),
		},
		"198.51.100.12": {
			User:           config.LinuxAccount,
			Host:           "198.51.100.12",
			Port:           2202,
			KnownHostsFile: filepath.Join(config.MacServiceRoot, "secrets", "queued-b_known_hosts"),
			IdentityFile:   filepath.Join(config.MacServiceRoot, "secrets", "queued-b_ed25519"),
		},
	}
	for _, got := range configured {
		want, ok := expected[got.Host]
		if !ok {
			t.Fatalf("factory was called for unexpected host %q", got.Host)
		}
		if got != want {
			t.Fatalf("pinned SSH config for %q = %+v, want %+v", got.Host, got, want)
		}
		delete(expected, got.Host)
	}
	if len(expected) != 0 {
		t.Fatalf("factory did not receive expected queued configs: %v", expected)
	}
	for profile, want := range map[string]*p151RouteCaller{"queued-a": callers["198.51.100.11"], "queued-b": callers["198.51.100.12"]} {
		got, err := resolver.ResolveRemoteCaller(profile)
		if err != nil || got != want {
			t.Fatalf("resolver profile %q returned caller=%v err=%v, want its exact pinned caller", profile, got, err)
		}
	}
	if _, err := resolver.ResolveRemoteCaller("direct-only"); !errors.Is(err, dispatcher.ErrRemoteRouteUnavailable) {
		t.Fatalf("direct-only profile resolution error = %v, want ErrRemoteRouteUnavailable", err)
	}
}

func TestP151RouterHealthKeepsEachQueuedProfileSeparate(t *testing.T) {
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	monitor := newRouterHealthMonitorForProfiles([]string{"queued-b", "queued-a"})
	secretBearingError := errors.New("ssh bridge failure for private-key=/secret/runner-key")
	monitor.updateProfiles(map[string]error{
		"queued-a": nil,
		"queued-b": secretBearingError,
	}, now)

	checks := remoteRouterHealthChecks(monitor, nil, now)
	if len(checks) != 2 {
		t.Fatalf("router health checks = %d, want 2: %+v", len(checks), checks)
	}
	byComponent := make(map[string]opshealth.Check, len(checks))
	for _, check := range checks {
		byComponent[check.Component] = check
	}
	if got := byComponent["remote_router/queued-a"]; got.State != opshealth.StateReady || got.Reason != "" {
		t.Fatalf("queued-a health check = %+v, want ready without an error reason", got)
	}
	if got := byComponent["remote_router/queued-b"]; got.State != opshealth.StateDegraded || got.Reason != "remote_transport_unavailable" {
		t.Fatalf("queued-b health check = %+v, want degraded remote_transport_unavailable", got)
	}
	encoded, err := json.Marshal(checks)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secretBearingError.Error()) || strings.Contains(string(encoded), "private-key=") {
		t.Fatalf("router health checks exposed a raw transport error: %s", encoded)
	}
}

func p151LoadOwnerOnlyConfig(t *testing.T, contents string) config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mac.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("temporary config mode = %o, want 600", got)
	}
	loaded, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("config.LoadFile(%q) error = %v", path, err)
	}
	return loaded
}

type p151RouteCaller struct{ name string }

func (c *p151RouteCaller) Call(context.Context, sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	return sshbridge.ReplyFrame{}, nil
}

const p151V1MacConfig = `version: 1
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
    allowed_targets: [{kind: local, profile: mac-workstation}]
    allowed_source_modes: [empty, local_worktree]
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
`

const p151V2MacConfig = `version: 2
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
  linux-a:
    base_system: Ubuntu A
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets: [{kind: remote, profile: queued-a}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers: [{type: queued_mac, id: tomasz.walczuk}]
  linux-b:
    base_system: Ubuntu B
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets: [{kind: remote, profile: queued-b}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers: [{type: queued_mac, id: tomasz.walczuk}]
execution_contexts:
  mac-local:
    environment: mac-dev
    execution_target: {kind: local, profile: mac-workstation}
  remote-a:
    environment: linux-a
    execution_target: {kind: remote, profile: queued-a}
  remote-b:
    environment: linux-b
    execution_target: {kind: remote, profile: queued-b}
remote_hosts:
  queued-a:
    account: ubuntu
    queued_bridge:
      host: 198.51.100.11
      port: 2201
      known_hosts: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/queued-a_known_hosts"
      private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/queued-a_ed25519"
  queued-b:
    account: ubuntu
    queued_bridge:
      host: 198.51.100.12
      port: 2202
      known_hosts: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/queued-b_known_hosts"
      private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/queued-b_ed25519"
  direct-only:
    account: ubuntu
    direct_endpoint:
      name: direct-only-poc
      url: https://198.51.100.13:8443
      server_ca: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-only-ca.pem"
      client_certificate: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-only-client.pem"
      client_private_key: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/secrets/direct-only-client.key"
mailboxes:
  default:
    root: "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox"
    repository_aliases: []
    default_execution: remote-a
    allowed_execution: [mac-local, remote-a, remote-b]
`
