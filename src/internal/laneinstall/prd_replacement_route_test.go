package laneinstall

import (
	"testing"

	"remote-session-runner/src/internal/config"
)

func TestPRDReplacementRouteRequiresExactReplacementHostIdentity(t *testing.T) {
	plan, err := BuildPRDUpgradePlan("linux-host")
	if err != nil {
		t.Fatal(err)
	}
	settings := config.LinuxSettings{
		Account: config.LinuxAccount, ServiceRoot: plan.Namespace.ServiceRoot, Database: plan.Namespace.Database,
		DirectHTTPSBind: prdReplacementLinuxBind, DirectPublicEndpoint: prdReplacementLinuxPublicURL,
	}
	if !prdLinuxRouteMatchesPlan(plan, settings, true) {
		t.Fatal("exact replacement route was not accepted for its replacement host")
	}
	if prdLinuxRouteMatchesPlan(plan, settings, false) {
		t.Fatal("replacement route was accepted without host identity evidence")
	}

	settings.DirectPublicEndpoint = "https://79.72.94.60:8443"
	if prdLinuxRouteMatchesPlan(plan, settings, true) {
		t.Fatal("unapproved replacement endpoint was accepted")
	}
	settings.DirectPublicEndpoint = prdReplacementLinuxPublicURL
	settings.DirectHTTPSBind = "10.0.0.229:8443"
	if prdLinuxRouteMatchesPlan(plan, settings, true) {
		t.Fatal("unapproved replacement bind was accepted")
	}
}

func TestConfiguredPRDHostIDAcceptsReplacementOnlyWithExactLocalIdentity(t *testing.T) {
	plan, err := BuildPRDUpgradePlan("linux-host")
	if err != nil {
		t.Fatal(err)
	}
	settings := config.LinuxSettings{
		Account: config.LinuxAccount, ServiceRoot: plan.Namespace.ServiceRoot, Database: plan.Namespace.Database,
		DirectHTTPSBind: prdReplacementLinuxBind, DirectPublicEndpoint: prdReplacementLinuxPublicURL,
	}
	if got, err := configuredPRDHostIDForLinux(settings, "linux", "amd64", config.LinuxAccount, []string{"10.0.0.228"}); err != nil || got != "linux-host" {
		t.Fatalf("exact replacement identity host=%q err=%v", got, err)
	}
	for _, tc := range []struct {
		name, goos, goarch, account string
		ips                         []string
	}{
		{name: "old host address", goos: "linux", goarch: "amd64", account: config.LinuxAccount, ips: []string{"10.0.0.200"}},
		{name: "wrong private address", goos: "linux", goarch: "amd64", account: config.LinuxAccount, ips: []string{"10.0.0.229"}},
		{name: "wrong account", goos: "linux", goarch: "amd64", account: "operator", ips: []string{"10.0.0.228"}},
		{name: "wrong architecture", goos: "linux", goarch: "arm64", account: config.LinuxAccount, ips: []string{"10.0.0.228"}},
		{name: "wrong platform", goos: "darwin", goarch: "arm64", account: config.LinuxAccount, ips: []string{"10.0.0.228"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := configuredPRDHostIDForLinux(settings, tc.goos, tc.goarch, tc.account, tc.ips); err == nil {
				t.Fatal("replacement PRD route matched without the exact host identity")
			}
		})
	}
}

func TestConfiguredPRDHostIDKeepsExistingCanonicalWorkerRoutes(t *testing.T) {
	for _, tc := range []struct{ hostID, goos, goarch string }{
		{"linux-host", "linux", "amd64"}, {"sandbox-host", "linux", "arm64"}, {"factory-sandbox", "linux", "amd64"},
	} {
		hostID := tc.hostID
		t.Run(hostID, func(t *testing.T) {
			plan, err := BuildPRDUpgradePlan(hostID)
			if err != nil {
				t.Fatal(err)
			}
			settings := config.LinuxSettings{
				Account: plan.Namespace.Account, ServiceRoot: plan.Namespace.ServiceRoot, Database: plan.Namespace.Database,
				DirectHTTPSBind: plan.Namespace.DirectHTTPSBind, DirectPublicEndpoint: plan.Namespace.DirectEndpointURL,
			}
			got, err := configuredPRDHostIDForLinux(settings, tc.goos, tc.goarch, config.LinuxAccount, nil)
			if err != nil || got != hostID {
				t.Fatalf("canonical host=%q err=%v", got, err)
			}
		})
	}
}

func TestV2PRDReplacementConfigurationResolvesOnlyOnReplacementWorker(t *testing.T) {
	contents := []byte(`version: 2
linux:
  account: ubuntu
  service_root: /home/ubuntu/.local/share/remote-session-runner
  sqlite: /home/ubuntu/.local/share/remote-session-runner/state/remote.db
  private_socket: /home/ubuntu/.local/share/remote-session-runner/run/runnerd.sock
  workspaces: /home/ubuntu/.local/share/remote-session-runner/workspaces
  script_temp_root: /home/ubuntu/.local/share/remote-session-runner/tmp/scripts
  backups: /home/ubuntu/.local/share/remote-session-runner/backups
  direct_https_bind: 10.0.0.228:8443
  direct_public_endpoint: https://79.72.94.59:8443
  server_cert: /home/ubuntu/.local/share/remote-session-runner/secrets/server.pem
  client_ca: /home/ubuntu/.local/share/remote-session-runner/secrets/client-ca.pem
  client_principal_map: /home/ubuntu/.local/share/remote-session-runner/config/client-principals.yaml
  tls_min_version: "1.3"
  runtime_adapter: linux-host-process
  remote_target_profile: linux-host
secret_references:
  linux_server_private_key:
    file: /home/ubuntu/.local/share/remote-session-runner/secrets/server.key
environment_registry:
  linux-prd:
    base_system: Ubuntu Linux
    host_class: Ubuntu Linux host
    effective_account: ubuntu
    allowed_targets: [{kind: remote, profile: linux-host}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers: [{type: direct_mtls, id: tomasz.walczuk}]
`)
	loaded, err := config.ParseBytes(contents)
	if err != nil {
		t.Fatalf("parse replacement PRD V2 config: %v", err)
	}
	if got, err := configuredPRDHostIDForConfiguration(loaded, "linux", "amd64", config.LinuxAccount, []string{"10.0.0.228"}); err != nil || got != "linux-host" {
		t.Fatalf("replacement V2 config resolved host=%q err=%v", got, err)
	}
	if _, err := configuredPRDHostIDForConfiguration(loaded, "linux", "amd64", config.LinuxAccount, []string{"10.0.0.200"}); err == nil {
		t.Fatal("replacement V2 config resolved on the retired worker address")
	}
}

func TestReplacementLinuxIdentityMatchesPIDIndependentAddressAndPlatformTuple(t *testing.T) {
	if !replacementLinuxIdentityMatches("linux-host", "linux", "amd64", config.LinuxAccount, []string{"127.0.0.1", "10.0.0.228"}) {
		t.Fatal("exact replacement host tuple was rejected")
	}
	for _, tc := range []struct {
		host, goos, goarch, account string
		ips                         []string
	}{
		{"sandbox-host", "linux", "amd64", config.LinuxAccount, []string{"10.0.0.228"}},
		{"linux-host", "linux", "amd64", config.LinuxAccount, []string{"10.0.0.200"}},
		{"linux-host", "linux", "amd64", "root", []string{"10.0.0.228"}},
		{"linux-host", "linux", "arm64", config.LinuxAccount, []string{"10.0.0.228"}},
		{"linux-host", "darwin", "arm64", config.LinuxAccount, []string{"10.0.0.228"}},
	} {
		if replacementLinuxIdentityMatches(tc.host, tc.goos, tc.goarch, tc.account, tc.ips) {
			t.Errorf("unexpected host identity accepted: %+v", tc)
		}
	}
}
