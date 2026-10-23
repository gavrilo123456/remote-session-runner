package laneinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
)

func writeLegacyProbeConfig(t *testing.T, environmentNames ...string) config.Config {
	return writeLegacyProbeConfigForTarget(t, "linux-host", environmentNames...)
}

func writeLegacyProbeConfigForTarget(t *testing.T, hostID string, environmentNames ...string) config.Config {
	t.Helper()
	var environments strings.Builder
	for _, name := range environmentNames {
		fmt.Fprintf(&environments, `  %s:
    base_system: Ubuntu
    host_class: Ubuntu Linux worker
    effective_account: ubuntu
    allowed_targets:
      - kind: remote
        profile: linux-host
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers:
      - type: direct_mtls
        id: tomasz.walczuk
      - type: queued_mac
        id: tomasz.walczuk
`, name)
	}
	contents := `version: 2
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
  remote_target_profile: linux-host
secret_references:
  linux_server_private_key:
    file: /home/ubuntu/.local/share/remote-session-runner/secrets/server.key
environment_registry:
` + environments.String()
	contents = strings.ReplaceAll(contents, "profile: linux-host", "profile: "+hostID)
	contents = strings.ReplaceAll(contents, "remote_target_profile: linux-host", "remote_target_profile: "+hostID)
	path := filepath.Join(t.TempDir(), "linux.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("load disposable legacy config: %v", err)
	}
	return loaded
}

func writeLegacyV1LinuxProbeConfig(t *testing.T) config.Config {
	t.Helper()
	contents := `version: 1
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
	path := filepath.Join(t.TempDir(), "linux-v1.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadFile(path)
	if err != nil || loaded.SchemaVersion() != config.VersionV1 {
		t.Fatalf("load V1 Linux config: schema=%d err=%v", loaded.SchemaVersion(), err)
	}
	return loaded
}

func writeLegacyV2MacProbeConfig(t *testing.T) config.Config {
	t.Helper()
	root := config.MacServiceRoot
	contents := fmt.Sprintf(`version: 2
mac:
  account: %s
  service_root: %q
  api_socket: %q
  locald_socket: %q
  sqlite: %q
  workspaces: %q
  script_temp_root: %q
  backups: %q
  mailbox_ingress_enabled: false
environment_registry:
  mac-production:
    base_system: macOS
    host_class: macOS workstation
    effective_account: %s
    allowed_targets: [{kind: local, profile: mac-workstation}]
    allowed_source_modes: [empty]
    allowed_repository_aliases: []
    allowed_controllers: [{type: local_user, id: %s}]
execution_contexts:
  mac-local-production:
    environment: mac-production
    execution_target: {kind: local, profile: mac-workstation}
`, config.MacAccount, root,
		filepath.Join(root, "run", "local-api.sock"), filepath.Join(root, "run", "locald.sock"),
		filepath.Join(root, "state", "local.db"), filepath.Join(root, "workspaces"),
		filepath.Join(root, "tmp", "scripts"), filepath.Join(root, "backups"), config.MacAccount, config.MacAccount)
	path := filepath.Join(t.TempDir(), "mac-v2.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadFile(path)
	if err != nil || loaded.SchemaVersion() != config.VersionV2 {
		t.Fatalf("load V2 Mac config: schema=%d err=%v", loaded.SchemaVersion(), err)
	}
	return loaded
}

func TestResolveLegacyProbeBindingUsesUniqueValidatedPolicy(t *testing.T) {
	plan, err := BuildCatalogPlan("linux-host", config.ReleaseLanePRD)
	if err != nil {
		t.Fatal(err)
	}
	loaded := writeLegacyProbeConfig(t, "linux-production")
	for _, transport := range []CatalogProbeTransport{CatalogProbeDirectMTLS, CatalogProbeQueuedSSH} {
		controllerType := domain.ControllerTypeDirectMTLS
		if transport == CatalogProbeQueuedSSH {
			controllerType = domain.ControllerTypeQueuedMac
		}
		binding, err := ResolveCatalogMaintenanceProbeBinding(loaded, plan, transport, controllerType, "tomasz.walczuk")
		if err != nil || binding.Environment != "linux-production" || binding.ConfigurationSHA != loaded.SourceSHA256() ||
			binding.TargetKind != "remote" || binding.TargetProfile != "linux-host" {
			t.Fatalf("%s legacy binding=%+v, err=%v", transport, binding, err)
		}
		body, err := CatalogMaintenanceProbeRequestBodyForBinding(plan, transport, binding)
		if err != nil {
			t.Fatalf("%s body: %v", transport, err)
		}
		var request catalogMaintenanceProbeRequest
		if json.Unmarshal(body, &request) != nil || request.Environment != "linux-production" {
			t.Fatalf("%s body did not use resolved legacy environment: %s", transport, body)
		}
	}
	if _, err := ResolveCatalogMaintenanceProbeBinding(loaded, plan, CatalogProbeDirectMTLS,
		domain.ControllerTypeDirectMTLS, "unconfigured-controller"); err == nil {
		t.Fatal("unconfigured authenticated controller resolved a maintenance environment")
	}

	v1Plan, err := BuildCatalogPlan("linux-host", config.ReleaseLanePRD)
	if err != nil {
		t.Fatal(err)
	}
	v1 := writeLegacyV1LinuxProbeConfig(t)
	for _, test := range []struct {
		transport  CatalogProbeTransport
		controller domain.ControllerType
	}{
		{CatalogProbeDirectMTLS, domain.ControllerTypeDirectMTLS},
		{CatalogProbeQueuedSSH, domain.ControllerTypeQueuedMac},
	} {
		binding, bindErr := ResolveCatalogMaintenanceProbeBinding(v1, v1Plan, test.transport, test.controller, "tomasz.walczuk")
		if bindErr != nil || binding.Environment != "linux-dev" || binding.ConfigurationSHA != v1.SourceSHA256() {
			t.Fatalf("V1 %s binding=%+v err=%v", test.transport, binding, bindErr)
		}
	}

	for _, hostID := range []string{"sandbox-host", "factory-sandbox"} {
		t.Run("V2/"+hostID, func(t *testing.T) {
			legacy := writeLegacyProbeConfigForTarget(t, hostID, "legacy-prd")
			legacyPlan, planErr := BuildCatalogPlan(hostID, config.ReleaseLanePRD)
			if planErr != nil {
				t.Fatal(planErr)
			}
			for _, test := range []struct {
				transport  CatalogProbeTransport
				controller domain.ControllerType
			}{
				{CatalogProbeDirectMTLS, domain.ControllerTypeDirectMTLS},
				{CatalogProbeQueuedSSH, domain.ControllerTypeQueuedMac},
			} {
				binding, bindErr := ResolveCatalogMaintenanceProbeBinding(legacy, legacyPlan, test.transport, test.controller, "tomasz.walczuk")
				if bindErr != nil || binding.Environment != "legacy-prd" || binding.TargetProfile != hostID {
					t.Errorf("V2 %s binding=%+v err=%v", test.transport, binding, bindErr)
				}
			}
		})
	}

	macPlan, err := BuildCatalogPlan("mac-control-plane", config.ReleaseLanePRD)
	if err != nil {
		t.Fatal(err)
	}
	mac := writeLegacyV2MacProbeConfig(t)
	macBinding, err := ResolveCatalogMaintenanceProbeBinding(mac, macPlan, CatalogProbeMacLocal,
		domain.ControllerTypeLocalUser, config.MacAccount)
	if err != nil || macBinding.Environment != "mac-production" || macBinding.TargetKind != "local" ||
		macBinding.TargetProfile != "mac-workstation" || macBinding.ConfigurationSHA != mac.SourceSHA256() {
		t.Fatalf("V2 Mac-local binding=%+v err=%v", macBinding, err)
	}
	ambiguous := writeLegacyProbeConfig(t, "linux-production", "linux-alias")
	if _, err := ResolveCatalogMaintenanceProbeBinding(ambiguous, plan, CatalogProbeDirectMTLS,
		domain.ControllerTypeDirectMTLS, "tomasz.walczuk"); err == nil {
		t.Fatal("ambiguous legacy environment match was accepted")
	}
}

func TestCatalogProbeEnvironmentCompatibilityAndScopeBinding(t *testing.T) {
	for _, test := range []struct {
		name         string
		schema       int
		environment  string
		target       string
		lane         config.ReleaseLane
		wantAccepted bool
	}{
		{name: "V1 legacy PRD policy name", schema: config.VersionV1, environment: "linux-dev", target: "linux-host", lane: config.ReleaseLanePRD, wantAccepted: true},
		{name: "V2 legacy arbitrary policy name", schema: config.VersionV2, environment: "mac-production", target: "mac-workstation", lane: config.ReleaseLanePRD, wantAccepted: true},
		{name: "V3 exact PRD lane", schema: config.VersionV3, environment: "linux-host-prd", target: "linux-host", lane: config.ReleaseLanePRD, wantAccepted: true},
		{name: "V3 rejects legacy environment", schema: config.VersionV3, environment: "linux-production", target: "linux-host", lane: config.ReleaseLanePRD},
		{name: "V3 rejects another lane", schema: config.VersionV3, environment: "linux-host-dev", target: "linux-host", lane: config.ReleaseLanePRD},
		{name: "V3 rejects another host", schema: config.VersionV3, environment: "sandbox-host-prd", target: "linux-host", lane: config.ReleaseLanePRD},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := catalogMaintenanceEnvironmentAllowed(test.schema, test.environment, test.target, test.lane); got != test.wantAccepted {
				t.Fatalf("environment allowed=%t, want %t", got, test.wantAccepted)
			}
		})
	}

	want := CatalogMaintenanceProbeBinding{Environment: "legacy-prd", ConfigurationSHA: strings.Repeat("a", 64)}
	scope := catalogProbeScope{Environment: want.Environment, ConfigurationSHA: want.ConfigurationSHA}
	if err := validateProbeScopeBinding(scope, want); err != nil {
		t.Fatalf("exact config binding refused: %v", err)
	}
	for _, mismatch := range []string{"environment", "configuration"} {
		t.Run("scope drift/"+mismatch, func(t *testing.T) {
			candidate := scope
			if mismatch == "environment" {
				candidate.Environment = "another-prd"
			} else {
				candidate.ConfigurationSHA = strings.Repeat("b", 64)
			}
			if err := validateProbeScopeBinding(candidate, want); err == nil {
				t.Fatal("scope bound to different configuration was accepted")
			}
		})
	}
}

type catalogProbeFixture struct {
	guard      *CatalogMaintenanceProbeGuard
	operation  string
	controller string
	key        string
	token      string
	body       []byte
	now        time.Time
}

func newCatalogProbeFixture(t *testing.T) catalogProbeFixture {
	t.Helper()
	selector := newCatalogSelectorFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	receipt, target := validCatalogCohortForSelectorFixture(t, selector, now)
	proof := strings.Repeat("f", 64)
	var err error
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "hold", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range catalogCohortSwitchOrder {
		receipt, err = AdvanceCatalogCohortReceipt(receipt, "preflight", host, proof, now)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err = AdvanceCatalogCohortReceipt(receipt, "switch", host, proof, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "verify-start", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range catalogCohortSwitchOrder {
		receipt, err = AdvanceCatalogCohortReceipt(receipt, "verify", host, proof, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	if receipt.Stage != CatalogCohortVerifying {
		t.Fatalf("fixture cohort stage=%q, want verifying", receipt.Stage)
	}
	if err := switchCatalogSelector(selector.layout, selector.legacyTarget, filepath.Join(selector.catalogRoot, target.ReleaseID, selector.layout.hostID)); err != nil {
		t.Fatal(err)
	}
	cohortRoot := filepath.Join(t.TempDir(), "catalog-cohorts")
	scopeRoot := filepath.Join(t.TempDir(), "catalog-probes")
	for _, root := range []string{cohortRoot, scopeRoot} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cohortData, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	cohortData = append(cohortData, '\n')
	if err := os.WriteFile(filepath.Join(cohortRoot, receipt.OperationID+".json"), cohortData, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildCatalogPlan("linux-host", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := NewCatalogMaintenanceProbeGuard(plan, receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	guard.testPaths = &catalogProbeTestPaths{cohortRoot: cohortRoot, scopeRoot: scopeRoot, layout: selector.layout}
	controller := "maintenance-operator"
	key := "catalog-probe-" + receipt.OperationID + "-linux-host"
	token := strings.Repeat("A", 43)
	body, err := CatalogMaintenanceProbeRequestBody(plan)
	if err != nil {
		t.Fatal(err)
	}
	tokenDigest := sha256.Sum256([]byte(token))
	bodyDigest := sha256.Sum256(body)
	expires := now.Add(time.Minute)
	scope := catalogProbeScope{
		FormatVersion: catalogProbeScopeFormat, OperationID: receipt.OperationID, HostID: plan.HostID, Lane: plan.Lane,
		Transport: CatalogProbeDirectMTLS, ControllerType: domain.ControllerTypeDirectMTLS,
		ControllerID: controller, Environment: "linux-host-dev", ConfigurationSHA: receipt.Hosts[cohortHostIndex(receipt, plan.HostID)].ConfigurationSHA256,
		CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: expires.Format(time.RFC3339Nano),
		TokenSHA256: hex.EncodeToString(tokenDigest[:]), IdempotencyKey: key,
		RequestBodySHA256: hex.EncodeToString(bodyDigest[:]), State: "ready",
	}
	if err := writeCatalogProbeScope(filepath.Join(scopeRoot, receipt.OperationID+".json"), scopeRoot, scope); err != nil {
		t.Fatal(err)
	}
	return catalogProbeFixture{guard: guard, operation: receipt.OperationID, controller: controller, key: key, token: token, body: body, now: now}
}

func TestCatalogMaintenanceProbeUsesTheSelectedHostLaneEnvironment(t *testing.T) {
	tests := []struct {
		hostID       string
		environments map[config.ReleaseLane]string
	}{
		{hostID: "linux-host", environments: map[config.ReleaseLane]string{
			config.ReleaseLaneDev: "linux-host-dev", config.ReleaseLaneQAS: "linux-host-qas", config.ReleaseLanePRD: "linux-host-prd",
		}},
		{hostID: "sandbox-host", environments: map[config.ReleaseLane]string{
			config.ReleaseLaneDev: "sandbox-host-dev", config.ReleaseLaneQAS: "sandbox-host-qas", config.ReleaseLanePRD: "sandbox-host-prd",
		}},
		{hostID: "factory-sandbox", environments: map[config.ReleaseLane]string{
			config.ReleaseLaneDev: "factory-sandbox-dev", config.ReleaseLaneQAS: "factory-sandbox-qas", config.ReleaseLanePRD: "factory-sandbox-prd",
		}},
	}
	for _, test := range tests {
		for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS, config.ReleaseLanePRD} {
			t.Run(test.hostID+"/"+string(lane), func(t *testing.T) {
				plan, err := BuildCatalogPlan(test.hostID, lane)
				if err != nil {
					t.Fatal(err)
				}
				body, err := CatalogMaintenanceProbeRequestBody(plan)
				if err != nil {
					t.Fatal(err)
				}
				var request catalogMaintenanceProbeRequest
				if err := json.Unmarshal(body, &request); err != nil {
					t.Fatal(err)
				}
				if want := test.environments[lane]; request.Environment != want {
					t.Fatalf("probe environment=%q, want configured lane environment %q", request.Environment, want)
				}
				if request.Target.Kind != "remote" || request.Target.Profile != test.hostID || request.Source.Mode != "empty" ||
					request.Script != "printf 'catalog release maintenance probe\\n'" || request.TimeoutSeconds != 5 {
					t.Fatalf("probe request changed its bounded target or command: %+v", request)
				}
			})
		}
	}
}

func TestCatalogMaintenanceProbeTransportBodiesRemainScoped(t *testing.T) {
	linuxPlan, err := BuildCatalogPlan("linux-host", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	for _, transport := range []CatalogProbeTransport{CatalogProbeDirectMTLS, CatalogProbeQueuedSSH} {
		body, err := CatalogMaintenanceProbeRequestBodyForTransport(linuxPlan, transport)
		if err != nil {
			t.Fatalf("%s body: %v", transport, err)
		}
		var request catalogMaintenanceProbeRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if request.Environment != "linux-host-dev" || request.Target.Kind != "remote" || request.Target.Profile != "linux-host" ||
			request.Source.Mode != "empty" || request.Script != "printf 'catalog release maintenance probe\\n'" {
			t.Fatalf("%s probe body changed its fixed command or target: %+v", transport, request)
		}
		if transport == CatalogProbeDirectMTLS && (request.TimeoutSeconds != 5 || request.Limits != nil) {
			t.Fatalf("direct probe must keep the direct API timeout field: %+v", request)
		}
		if transport == CatalogProbeQueuedSSH {
			if len(request.Limits) != 1 || request.Limits["command_timeout_seconds"] != 5 || request.TimeoutSeconds != 0 {
				t.Fatalf("queued probe must use the bridge limits shape: %+v", request)
			}
		}
	}
	if _, err := CatalogMaintenanceProbeRequestBodyForTransport(linuxPlan, CatalogProbeMacLocal); err == nil {
		t.Fatal("Mac-local probe body was accepted for a Linux host")
	}

	macPlan, err := BuildCatalogPlan("mac-control-plane", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	body, err := CatalogMaintenanceProbeRequestBodyForTransport(macPlan, CatalogProbeMacLocal)
	if err != nil {
		t.Fatal(err)
	}
	var localRequest catalogMaintenanceProbeRequest
	if err := json.Unmarshal(body, &localRequest); err != nil {
		t.Fatal(err)
	}
	if localRequest.Environment != "mac-workstation-dev" || localRequest.Target.Kind != "local" || localRequest.Target.Profile != "mac-workstation" ||
		localRequest.Source.Mode != "empty" || localRequest.Script != "printf 'catalog release maintenance probe\\n'" || localRequest.TimeoutSeconds != 5 {
		t.Fatalf("Mac-local probe body changed its fixed command or configured target: %+v", localRequest)
	}
	if _, err := CatalogMaintenanceProbeRequestBodyForTransport(macPlan, CatalogProbeQueuedSSH); err == nil {
		t.Fatal("queued SSH probe body was accepted for the Mac host")
	}
}

func TestCatalogMaintenanceProbeControllerTypeIsBoundToTransport(t *testing.T) {
	tests := []struct {
		transport CatalogProbeTransport
		typeName  domain.ControllerType
		want      bool
	}{
		{CatalogProbeDirectMTLS, domain.ControllerTypeDirectMTLS, true},
		{CatalogProbeDirectMTLS, domain.ControllerTypeQueuedMac, false},
		{CatalogProbeQueuedSSH, domain.ControllerTypeQueuedMac, true},
		{CatalogProbeQueuedSSH, domain.ControllerTypeDirectMTLS, false},
		{CatalogProbeMacLocal, domain.ControllerTypeLocalUser, true},
		{CatalogProbeMacLocal, domain.ControllerTypeQueuedMac, false},
	}
	for _, test := range tests {
		if got := catalogProbeControllerAllowed(test.transport, test.typeName); got != test.want {
			t.Errorf("transport %q controller type %q allowed=%t, want %t", test.transport, test.typeName, got, test.want)
		}
	}
}

func TestCatalogMaintenanceProbeIsOneShotAndJobReadIsIdentityBound(t *testing.T) {
	fixture := newCatalogProbeFixture(t)
	if err := fixture.guard.ReserveCatalogMaintenanceProbe(fixture.controller, fixture.token, fixture.key, fixture.body, fixture.now); err != nil {
		t.Fatalf("reserve exact probe: %v", err)
	}
	if err := fixture.guard.ReserveCatalogMaintenanceProbe(fixture.controller, fixture.token, fixture.key, fixture.body, fixture.now); err == nil {
		t.Fatal("replayed probe reservation was accepted")
	}
	jobID := "job-0123456789abcdef0123456789abcdef"
	if err := fixture.guard.BindCatalogMaintenanceProbeJob(jobID); err != nil {
		t.Fatalf("bind accepted job identity: %v", err)
	}
	if fixture.guard.AllowsReservedJobIntent(domain.ControllerTypeDirectMTLS, fixture.controller, fixture.token, "intent-0123456789abcdef0123456789abcdef", jobID, fixture.now) {
		t.Fatal("unbound intent unexpectedly passed the stricter job-intent guard")
	}
	if !fixture.guard.AllowsJobRead(fixture.controller, jobID, fixture.now) {
		t.Fatal("exact reserved probe job was not readable")
	}
	if fixture.guard.AllowsJobRead("another-controller", jobID, fixture.now) ||
		fixture.guard.AllowsJobRead(fixture.controller, "job-ffffffffffffffffffffffffffffffff", fixture.now) {
		t.Fatal("scope allowed an unrelated controller or job read")
	}
	if err := fixture.guard.MarkCatalogMaintenanceProbeAccepted(jobID); err != nil {
		t.Fatalf("mark durable acceptance: %v", err)
	}
	if !fixture.guard.AllowsJobRead(fixture.controller, jobID, fixture.now) {
		t.Fatal("accepted probe job became unreadable")
	}
	if fixture.guard.AllowsJobRead(fixture.controller, jobID, fixture.now.Add(time.Minute)) {
		t.Fatal("expired probe scope allowed a job read")
	}
	if err := fixture.guard.MarkCatalogMaintenanceProbeAccepted(jobID); err == nil {
		t.Fatal("acceptance state transition was not one-shot")
	}
}

func TestCatalogMaintenanceProbeRejectsMismatchedEvidenceWithoutConsumingScope(t *testing.T) {
	for _, mismatch := range []string{"controller", "token", "idempotency", "body"} {
		t.Run(mismatch, func(t *testing.T) {
			fixture := newCatalogProbeFixture(t)
			controller, token, key, body := fixture.controller, fixture.token, fixture.key, fixture.body
			switch mismatch {
			case "controller":
				controller = "other-controller"
			case "token":
				token = strings.Repeat("B", 43)
			case "idempotency":
				key += "-changed"
			case "body":
				body = append(append([]byte(nil), body...), ' ')
			}
			if err := fixture.guard.ReserveCatalogMaintenanceProbe(controller, token, key, body, fixture.now); err == nil {
				t.Fatal("mismatched evidence was accepted")
			}
			if err := fixture.guard.ReserveCatalogMaintenanceProbe(fixture.controller, fixture.token, fixture.key, fixture.body, fixture.now); err != nil {
				t.Fatalf("refused mismatch consumed the valid scope: %v", err)
			}
		})
	}
}

func TestCatalogMaintenanceProbeFailsClosedAfterExpiryOrCohortChange(t *testing.T) {
	fixture := newCatalogProbeFixture(t)
	if err := fixture.guard.ReserveCatalogMaintenanceProbe(fixture.controller, fixture.token, fixture.key, fixture.body, fixture.now.Add(time.Minute)); err == nil {
		t.Fatal("expired probe reservation was accepted")
	}
	path := filepath.Join(fixture.guard.testPaths.cohortRoot, fixture.operation+".json")
	receipt, err := readCatalogCohortReceipt(path)
	if err != nil {
		t.Fatal(err)
	}
	proof := strings.Repeat("d", 64)
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "complete", "", proof, fixture.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.guard.ReserveCatalogMaintenanceProbe(fixture.controller, fixture.token, fixture.key, fixture.body, fixture.now.Add(time.Minute)); err == nil {
		t.Fatal("cohort completion did not close the probe admission window")
	}
}
