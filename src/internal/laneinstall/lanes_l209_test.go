package laneinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/lanecapacity"
)

func TestLanesL209PreflightBuildsFixedMacAndLinuxLanePlans(t *testing.T) {
	for _, host := range []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"} {
		for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS} {
			input := lanesL209Input(t, host, lane)
			plan, err := BuildLanePreflightPlan(input)
			if err != nil {
				t.Fatalf("preflight %s/%s: %v", host, lane, err)
			}
			report, err := plan.Report()
			if err != nil {
				t.Fatal(err)
			}
			if report.Status != "validated_dry_run" || report.Action != "none" || report.HostID != host || report.Lane != lane ||
				report.NativeAcceptance != "not_performed" || report.ProductionChanges != "none" || report.ArtifactCount != len(plan.Artifacts()) {
				t.Fatalf("unsafe or incomplete preflight report for %s/%s: %+v", host, lane, report)
			}
			wantQueuePort := 2224
			if lane == config.ReleaseLaneQAS {
				wantQueuePort = 2225
			}
			if !reflect.DeepEqual(report.QueuePorts, []int{wantQueuePort}) {
				t.Errorf("%s/%s queue ports=%v, want only %d", host, lane, report.QueuePorts, wantQueuePort)
			}
			if host == "mac-control-plane" {
				if report.ResourceControls != "measured-apfs-quota-and-headroom" || len(report.DirectEndpoints) != 3 || report.ArtifactCount != 2 {
					t.Errorf("Mac %s preflight omitted routes or APFS controls: %+v", lane, report)
				}
			} else if report.ResourceControls != "linux-cgroup-v2-and-project-quota" || len(report.DirectEndpoints) != 1 || report.ArtifactCount != 8 {
				t.Errorf("Linux %s/%s preflight omitted measured controls: %+v", host, lane, report)
			}
			encoded, err := jsonMarshal(report)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"client.key", "server.key", "sha256:", "/authorized_keys", "Port 22"} {
				if strings.Contains(encoded, forbidden) {
					t.Errorf("safe report disclosed or selected %q: %s", forbidden, encoded)
				}
			}
		}
	}
}

func TestLanesL209RejectsForeignLaneHostAndResourceSelection(t *testing.T) {
	input := lanesL209Input(t, "linux-host", config.ReleaseLaneDev)
	if _, err := BuildLanePreflightPlan(input); err != nil {
		t.Fatal(err)
	}

	foreign := input
	foreign.Lane = config.ReleaseLanePRD
	if _, err := BuildLanePreflightPlan(foreign); !errors.Is(err, ErrInvalidPreflight) {
		t.Errorf("PRD lane selection error=%v", err)
	}
	foreign = input
	foreign.HostID = "sandbox-host"
	if _, err := BuildLanePreflightPlan(foreign); !errors.Is(err, ErrInvalidPreflight) {
		t.Errorf("foreign host selection error=%v", err)
	}
	foreign = input
	foreign.Policy.Scope = lanecapacity.ScopeHostManagement
	foreign.Policy.LaneRuntime = nil
	foreign.Policy.HostManagement = lanesL207ManagementBudget(input.Measurement)
	if _, err := BuildLanePreflightPlan(foreign); !errors.Is(err, ErrInvalidPreflight) {
		t.Errorf("management-only policy accepted for a lane: %v", err)
	}
	foreign = input
	foreign.Policy.Lane = string(config.ReleaseLaneQAS)
	if _, err := BuildLanePreflightPlan(foreign); !errors.Is(err, ErrInvalidPreflight) {
		t.Errorf("foreign lane capacity policy accepted: %v", err)
	}

	prd, err := config.DeriveLaneNamespace(config.HostKindLinux, config.ReleaseLanePRD, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	macBindings, err := config.ValidateV3ProfileBindings(lanesL209MacBindings(t, config.ReleaseLaneDev))
	if err != nil {
		t.Fatal(err)
	}
	badProfiles := lanesL209LinuxBindings(t, config.ReleaseLaneDev, "linux-host", macBindings)
	badProfiles.LinuxProfile.Direct.ServerPrivateKeyFile = filepath.Join(prd.ServiceRoot, "secrets", "server.key")
	if _, err := config.ValidateV3ProfileBindings(badProfiles); !errors.Is(err, config.ErrInvalidLaneProfiles) {
		t.Errorf("PRD certificate path accepted by profile validation: %v", err)
	}

	pairInput := lanesL209Input(t, "linux-host", config.ReleaseLaneDev)
	macPeer, err := config.ValidateV3ProfileBindings(lanesL209MacBindings(t, config.ReleaseLaneDev))
	if err != nil {
		t.Fatal(err)
	}
	foreignLinux := lanesL209LinuxBindings(t, config.ReleaseLaneDev, "linux-host", macPeer)
	foreignLinux.LinuxProfile.Direct.AuthorizedClientFingerprint = lanesL209Fingerprint(999)
	validatedForeignLinux, err := config.ValidateV3ProfileBindings(foreignLinux)
	if err != nil {
		t.Fatalf("isolated Linux profile fixture should remain internally valid: %v", err)
	}
	pairInput.Profiles = validatedForeignLinux
	pairInput.PeerProfiles = []config.ValidatedV3Profiles{macPeer}
	if _, err := BuildLanePreflightPlan(pairInput); !errors.Is(err, ErrInvalidPreflight) {
		t.Errorf("mismatched Mac/Linux certificate pair accepted: %v", err)
	}
}

func TestLanesL209StagedStartFailureRollsBackFixtureAndPreservesPRD(t *testing.T) {
	plan, err := BuildLanePreflightPlan(lanesL209Input(t, "linux-host", config.ReleaseLaneDev))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	backend := newLanesL209StageFixture(t, func() { cancel() })
	backend.failAt = "start"
	err = RunStagedStart(ctx, plan, backend)
	if !errors.Is(err, ErrStagedStart) || errors.Is(err, ErrRollbackUnverified) {
		t.Fatalf("staged start failure result=%v", err)
	}
	if !reflect.DeepEqual(backend.events, []string{"prepare", "stage", "start-closed", "rollback", "verify-rollback"}) {
		t.Fatalf("staged-start failure sequence=%v", backend.events)
	}
	if !backend.restored || backend.prdAfter != backend.prdBefore || backend.globalSSHAfter != backend.globalSSHBefore {
		t.Fatalf("rollback did not preserve fixture state: restored=%v PRD=%q/%q ssh=%q/%q", backend.restored, backend.prdBefore, backend.prdAfter, backend.globalSSHBefore, backend.globalSSHAfter)
	}
}

func TestLanesL209SuccessfulStageRemainsAdmissionClosed(t *testing.T) {
	plan, err := BuildLanePreflightPlan(lanesL209Input(t, "factory-sandbox", config.ReleaseLaneQAS))
	if err != nil {
		t.Fatal(err)
	}
	backend := newLanesL209StageFixture(t, nil)
	if err := RunStagedStart(context.Background(), plan, backend); err != nil {
		t.Fatal(err)
	}
	if !backend.closedHealthy || backend.openCalled || !reflect.DeepEqual(backend.events, []string{"prepare", "stage", "start-closed", "verify-closed"}) {
		t.Fatalf("staged candidate was not left healthy and closed: events=%v closed=%v open=%v", backend.events, backend.closedHealthy, backend.openCalled)
	}
}

func TestLanesL209RollbackFailureIsReportedAndStillVerified(t *testing.T) {
	plan, err := BuildLanePreflightPlan(lanesL209Input(t, "linux-host", config.ReleaseLaneQAS))
	if err != nil {
		t.Fatal(err)
	}
	backend := newLanesL209StageFixture(t, nil)
	backend.failAt, backend.failRollback = "health", true
	err = RunStagedStart(context.Background(), plan, backend)
	if !errors.Is(err, ErrRollbackUnverified) || !backend.verifyRollbackCalled {
		t.Fatalf("rollback failure was not surfaced and checked: err=%v verified=%v", err, backend.verifyRollbackCalled)
	}
	if backend.openCalled || backend.prdAfter != backend.prdBefore || backend.globalSSHAfter != backend.globalSSHBefore {
		t.Fatalf("failed rollback escaped its fixture or touched protected state: %+v", backend)
	}
}

func TestLanesL209RejectsMutatedPlanBeforeStaging(t *testing.T) {
	plan, err := BuildLanePreflightPlan(lanesL209Input(t, "sandbox-host", config.ReleaseLaneDev))
	if err != nil {
		t.Fatal(err)
	}
	artifacts := plan.Artifacts()
	artifacts[0].InstallPath = "/etc/systemd/system/runnerd.service"
	if plan.Artifacts()[0].InstallPath == artifacts[0].InstallPath {
		t.Fatal("artifact accessor exposed mutable plan storage")
	}
	plan.artifacts[0].InstallPath = "/etc/systemd/system/runnerd.service"
	backend := newLanesL209StageFixture(t, nil)
	if err := RunStagedStart(context.Background(), plan, backend); !errors.Is(err, ErrInvalidPreflight) {
		t.Fatalf("mutated plan accepted: %v", err)
	}
	if len(backend.events) != 0 || len(backend.stagedPaths) != 0 {
		t.Fatalf("mutated plan reached stage backend: events=%v paths=%v", backend.events, backend.stagedPaths)
	}
}

func lanesL209Input(t *testing.T, host string, lane config.ReleaseLane) PreflightInput {
	t.Helper()
	var measurement lanecapacity.Measurement
	var live lanecapacity.LiveObservation
	var policy lanecapacity.Policy
	var now time.Time
	var bindings config.ValidatedV3Profiles
	var peers []config.ValidatedV3Profiles
	var err error
	if host == "mac-control-plane" {
		measurement, live, policy, now = macL208Fixture(t, lane)
		bindings, err = config.ValidateV3ProfileBindings(lanesL209MacBindings(t, lane))
		if err == nil {
			for _, profile := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
				peer, peerErr := config.ValidateV3ProfileBindings(lanesL209LinuxBindings(t, lane, profile, bindings))
				if peerErr != nil {
					err = peerErr
					break
				}
				peers = append(peers, peer)
			}
		}
	} else {
		measurement, live, policy, now = lanesL207Fixture(t, host)
		policy.Lane = string(lane)
		var mac config.ValidatedV3Profiles
		mac, err = config.ValidateV3ProfileBindings(lanesL209MacBindings(t, lane))
		if err == nil {
			peers = []config.ValidatedV3Profiles{mac}
			bindings, err = config.ValidateV3ProfileBindings(lanesL209LinuxBindings(t, lane, host, mac))
		}
	}
	if err != nil {
		t.Fatalf("validate synthetic %s/%s profile fixture: %v", host, lane, err)
	}
	return PreflightInput{HostID: host, Lane: lane, Profiles: bindings, PeerProfiles: peers, Policy: policy, Measurement: measurement, Live: live, Now: now}
}

func lanesL209MacBindings(t *testing.T, lane config.ReleaseLane) config.V3ProfileBindings {
	t.Helper()
	macNS, err := config.DeriveLaneNamespace(config.HostKindMac, lane, "")
	if err != nil {
		t.Fatal(err)
	}
	bindings := config.V3ProfileBindings{Version: config.VersionV3, ReleaseLane: lane, Host: config.HostKindMac}
	for index, profile := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		remoteNS, err := config.DeriveLaneNamespace(config.HostKindLinux, lane, profile)
		if err != nil {
			t.Fatal(err)
		}
		base := lanesL209Fingerprint(100 + index*10 + int(laneNumber(lane))*100)
		controller := "runner-controller-" + string(lane) + "-" + profile
		secret := func(parts ...string) string {
			return filepath.Join(append([]string{macNS.ServiceRoot, "secrets", "profiles", profile}, parts...)...)
		}
		bindings.MacProfiles = append(bindings.MacProfiles, config.V3MacRemoteProfile{
			Profile: profile,
			Queued: config.V3MacQueuedCredentials{Host: endpointIP(t, remoteNS.DirectEndpointURL), Port: remoteNS.QueuePort,
				KnownHostsFile: secret("ssh", "known_hosts"), HostKeyFingerprint: base,
				DispatcherPrivateKeyFile: secret("ssh", "dispatcher_ed25519"), DispatcherPublicKeyFingerprint: lanesL209Fingerprint(101 + index*10 + int(laneNumber(lane))*100)},
			Direct: config.V3MacDirectCredentials{Name: remoteNS.DirectEndpointName, URL: remoteNS.DirectEndpointURL,
				ServerCAFile: secret("tls", "server-ca.pem"), ServerCAFingerprint: lanesL209Fingerprint(102 + index*10 + int(laneNumber(lane))*100),
				ClientCertificateFile: secret("tls", "client.pem"), ClientCertificateFingerprint: lanesL209Fingerprint(103 + index*10 + int(laneNumber(lane))*100),
				ClientPrivateKeyFile: secret("tls", "client.key"), ClientSPKIFingerprint: lanesL209Fingerprint(104 + index*10 + int(laneNumber(lane))*100),
				ExpectedServerCertificateFingerprint: lanesL209Fingerprint(105 + index*10 + int(laneNumber(lane))*100),
				ExpectedClientCAFingerprint:          lanesL209Fingerprint(106 + index*10 + int(laneNumber(lane))*100),
				ClientURISAN:                         "urn:remote-session-runner:lane:" + string(lane) + ":controller:" + profile,
				ServerURISAN:                         "urn:remote-session-runner:lane:" + string(lane) + ":server:" + profile,
				ControllerType:                       "direct_mtls", ControllerID: controller},
		})
		bindings.Environments = append(bindings.Environments, config.V3EnvironmentBinding{
			Name: profile + "-dev", EffectiveAccount: config.LinuxAccount,
			AllowedTargets:     []config.V3TargetBinding{{Kind: domain.TargetKindRemote, Profile: profile}},
			AllowedControllers: []config.V3ControllerBinding{{Type: "queued_mac", ID: config.MacAccount}, {Type: "direct_mtls", ID: controller}},
		})
		bindings.ExecutionContexts = append(bindings.ExecutionContexts, config.V3ExecutionContextBinding{
			Name: profile + "-context", Environment: profile + "-dev",
			Target: config.V3TargetBinding{Kind: domain.TargetKindRemote, Profile: profile},
		})
	}
	return bindings
}

func lanesL209LinuxBindings(t *testing.T, lane config.ReleaseLane, profile string, mac config.ValidatedV3Profiles) config.V3ProfileBindings {
	t.Helper()
	ns, err := config.DeriveLaneNamespace(config.HostKindLinux, lane, profile)
	if err != nil {
		t.Fatal(err)
	}
	secret := func(parts ...string) string {
		return filepath.Join(append([]string{ns.ServiceRoot, "secrets", "profiles", profile}, parts...)...)
	}
	endpoint := endpointIP(t, ns.DirectEndpointURL)
	bindHost, _, err := net.SplitHostPort(ns.DirectHTTPSBind)
	if err != nil {
		t.Fatal(err)
	}
	controller := "runner-controller-" + string(lane) + "-" + profile
	macProfile, ok := mac.MacProfile(profile)
	if !ok {
		t.Fatalf("Mac profile fixture missing %s", profile)
	}
	bindings := config.V3ProfileBindings{Version: config.VersionV3, ReleaseLane: lane, Host: config.HostKindLinux, RemoteProfile: profile,
		LinuxProfile: &config.V3LinuxProfileBinding{RemoteProfile: profile,
			Queued: config.V3LinuxQueuedCredentials{Host: endpoint, Port: ns.QueuePort, Bind: net.JoinHostPort(bindHost, fmt.Sprint(ns.QueuePort)),
				HostKeyFile: filepath.Join(ns.SSHAuthorityPath, "ssh_host_ed25519_key"), HostKeyFingerprint: macProfile.Queued.HostKeyFingerprint,
				AuthorizedKeyFingerprint: macProfile.Queued.DispatcherPublicKeyFingerprint, Socket: ns.PrivateSocket},
			Direct: config.V3LinuxDirectCredentials{Name: ns.DirectEndpointName, URL: ns.DirectEndpointURL, Bind: ns.DirectHTTPSBind,
				ServerCertificateFile: secret("tls", "server.pem"), ServerCertificateFingerprint: macProfile.Direct.ExpectedServerCertificateFingerprint,
				ServerPrivateKeyFile: secret("tls", "server.key"), ServerSPKIFingerprint: lanesL209Fingerprint(213 + int(laneNumber(lane))*10),
				ClientCAFile: secret("tls", "client-ca.pem"), ClientCAFingerprint: macProfile.Direct.ExpectedClientCAFingerprint,
				ClientPrincipalMapFile:          filepath.Join(ns.ServiceRoot, "config", "client-principals.yaml"),
				AuthorizedClientFingerprint:     macProfile.Direct.ClientCertificateFingerprint,
				AuthorizedClientSPKIFingerprint: macProfile.Direct.ClientSPKIFingerprint,
				ClientURISAN:                    macProfile.Direct.ClientURISAN,
				ServerURISAN:                    macProfile.Direct.ServerURISAN,
				ControllerType:                  macProfile.Direct.ControllerType, ControllerID: controller}},
		Environments: []config.V3EnvironmentBinding{{Name: profile + "-dev", EffectiveAccount: config.LinuxAccount,
			AllowedTargets:     []config.V3TargetBinding{{Kind: domain.TargetKindRemote, Profile: profile}},
			AllowedControllers: []config.V3ControllerBinding{{Type: "queued_mac", ID: config.MacAccount}, {Type: "direct_mtls", ID: controller}}}},
		ExecutionContexts: []config.V3ExecutionContextBinding{{Name: profile + "-local-context", Environment: profile + "-dev",
			Target: config.V3TargetBinding{Kind: domain.TargetKindRemote, Profile: profile}}},
	}
	return bindings
}

func lanesL209Fingerprint(value int) string { return fmt.Sprintf("sha256:%064x", value) }

func laneNumber(lane config.ReleaseLane) int {
	if lane == config.ReleaseLaneQAS {
		return 1
	}
	return 0
}

func endpointIP(t *testing.T, endpoint string) string {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Hostname()
}

func jsonMarshal(value any) (string, error) {
	data, err := json.Marshal(value)
	return string(data), err
}

type lanesL209StageFixture struct {
	t                    *testing.T
	root                 string
	before               map[string]fixtureFile
	events               []string
	failAt               string
	failRollback         bool
	cancel               context.CancelFunc
	restored             bool
	prdBefore            string
	prdAfter             string
	globalSSHBefore      string
	globalSSHAfter       string
	closedHealthy        bool
	openCalled           bool
	verifyRollbackCalled bool
	stagedPaths          []string
}

type fixtureFile struct {
	data []byte
	mode os.FileMode
}

func newLanesL209StageFixture(t *testing.T, cancel context.CancelFunc) *lanesL209StageFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &lanesL209StageFixture{t: t, root: root, cancel: cancel}
	prdPath := filepath.Join(root, "protected", "prd", "runnerd.service")
	globalSSHPath := filepath.Join(root, "protected", "ssh", "authorized_keys")
	for path, content := range map[string]string{prdPath: "original-prd-service\n", globalSSHPath: "original-port-22-key\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	devPath := filepath.Join(root, "install", "etc", "systemd", "system", "remote-session-runner-dev.service")
	if err := os.MkdirAll(filepath.Dir(devPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(devPath, []byte("prior-dev-service\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.before = fixture.snapshot()
	fixture.prdBefore = string(fixture.before[prdPath].data)
	fixture.globalSSHBefore = string(fixture.before[globalSSHPath].data)
	return fixture
}

func (f *lanesL209StageFixture) PrepareRollback(ctx context.Context, plan LanePreflightPlan) (StageReceipt, error) {
	f.events = append(f.events, "prepare")
	if ctx.Err() != nil || !plan.valid() {
		return "", errors.New("fixture refused invalid rollback preflight")
	}
	return StageReceipt("fixture-stage-001"), nil
}

func (f *lanesL209StageFixture) StageCandidate(_ context.Context, receipt StageReceipt, artifacts []Artifact) error {
	f.events = append(f.events, "stage")
	if receipt != "fixture-stage-001" {
		return errors.New("fixture receipt mismatch")
	}
	for _, artifact := range artifacts {
		path := f.pathFor(artifact.InstallPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(artifact.Contents), os.FileMode(artifact.Mode)); err != nil {
			return err
		}
		f.stagedPaths = append(f.stagedPaths, artifact.InstallPath)
	}
	if f.failAt == "stage" {
		return errors.New("fixture staged write failed")
	}
	return nil
}

func (f *lanesL209StageFixture) StartCandidateClosed(context.Context, StageReceipt) error {
	f.events = append(f.events, "start-closed")
	if f.cancel != nil {
		f.cancel()
	}
	if f.failAt == "start" {
		return errors.New("fixture closed start failed")
	}
	return nil
}

func (f *lanesL209StageFixture) VerifyCandidateHealthyClosed(context.Context, StageReceipt) error {
	f.events = append(f.events, "verify-closed")
	if f.failAt == "health" {
		return errors.New("fixture closed health check failed")
	}
	f.closedHealthy = true
	return nil
}

func (f *lanesL209StageFixture) RollbackCandidate(context.Context, StageReceipt) error {
	f.events = append(f.events, "rollback")
	if f.failRollback {
		return errors.New("fixture rollback error")
	}
	if err := os.RemoveAll(f.root); err != nil {
		return err
	}
	if err := os.MkdirAll(f.root, 0o700); err != nil {
		return err
	}
	for path, file := range f.before {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, file.data, file.mode); err != nil {
			return err
		}
	}
	return nil
}

func (f *lanesL209StageFixture) VerifyRollback(context.Context, StageReceipt) error {
	f.events = append(f.events, "verify-rollback")
	f.verifyRollbackCalled = true
	after := f.snapshot()
	f.restored = reflect.DeepEqual(after, f.before)
	prdPath := filepath.Join(f.root, "protected", "prd", "runnerd.service")
	globalSSHPath := filepath.Join(f.root, "protected", "ssh", "authorized_keys")
	f.prdAfter = string(after[prdPath].data)
	f.globalSSHAfter = string(after[globalSSHPath].data)
	if f.failRollback || !f.restored {
		return errors.New("fixture state differs after rollback")
	}
	return nil
}

func (f *lanesL209StageFixture) snapshot() map[string]fixtureFile {
	f.t.Helper()
	files := make(map[string]fixtureFile)
	err := filepath.WalkDir(f.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = fixtureFile{data: data, mode: info.Mode()}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return files
}

func (f *lanesL209StageFixture) pathFor(installPath string) string {
	return filepath.Join(f.root, "install", strings.TrimPrefix(filepath.Clean(installPath), string(filepath.Separator)))
}

var _ StagedStartBackend = (*lanesL209StageFixture)(nil)
