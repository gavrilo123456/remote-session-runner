//go:build p128opshost

package runnerlocal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/localapi"
	"remote-session-runner/src/internal/mailbox"
	"remote-session-runner/src/internal/opshealth"
	"remote-session-runner/src/internal/sshbridge"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestP128MacIngressAcceptsDurableIntentDuringRemoteOutage(t *testing.T) {
	if os.Getenv("RSR_P128_OPS_HOST_GATE") != "1" {
		t.Skip("set RSR_P128_OPS_HOST_GATE=1 to run the actual Mac/Ubuntu P-OPS-01 gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P128 Mac ingress gate must run on macOS, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "tomasz.walczuk" {
		t.Fatalf("P128 Mac account=%v err=%v, want tomasz.walczuk", current, err)
	}
	identity := strings.TrimSpace(os.Getenv("RUNNER_P128_SSH_IDENTITY"))
	knownHosts := strings.TrimSpace(os.Getenv("RUNNER_P128_SSH_KNOWN_HOSTS"))
	for name, path := range map[string]string{"RUNNER_P128_SSH_IDENTITY": identity, "RUNNER_P128_SSH_KNOWN_HOSTS": knownHosts} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			t.Fatalf("%s must be an absolute selected fixture path", name)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("%s cannot be inspected: %v", name, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || uint32(stat.Uid) != uint32(os.Geteuid()) || info.Mode().Perm()&0o400 == 0 || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s must be an owner-readable regular file with owner-only permissions", name)
		}
	}
	t.Logf("machine=Mac account=%s uid=%s os=%s go=%s remote=ubuntu@129.151.232.40", current.Username, current.Uid, runtime.GOOS, runtime.Version())
	p128CheckMacIngressDoctor(t, os.Getenv("RUNNER_P128_MAC_CONFIG"))

	ssh, err := sshclient.New(sshclient.Config{User: "ubuntu", Host: "129.151.232.40", IdentityFile: identity, KnownHostsFile: knownHosts})
	if err != nil {
		t.Fatal(err)
	}
	caller := &p128SwitchableSSHCaller{client: ssh}
	remoteAuthorityRoot := testfixture.New(t)
	db, err := store.Open(context.Background(), filepath.Join(remoteAuthorityRoot.Path(), "state", "mac-intent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := dispatcher.NewRemoteDriver(authority, caller, "mac-router-p128-host", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	monitor := newRouterHealthMonitor()
	service := &Service{database: authority, remoteDriver: remote, routerHealth: monitor, remoteProbe: remote.Probe}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	healthy := service.Doctor(ctx)
	if healthy.Readiness != opshealth.StateReady || len(healthy.Checks) != 2 || healthy.Checks[1].State != opshealth.StateReady {
		t.Fatalf("actual pinned SSH/Ubuntu probe did not establish a healthy baseline: %+v", healthy)
	}
	caller.setUnavailable(true)
	degraded := service.Doctor(ctx)
	if degraded.Readiness != opshealth.StateReady || degraded.Checks[1].State != opshealth.StateDegraded || degraded.Checks[1].Reason != "remote_transport_unavailable" {
		t.Fatalf("Mac doctor did not separate remote outage from ingress readiness: %+v", degraded)
	}

	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	apiRoot, err := os.MkdirTemp("/private/tmp", "p128-api-")
	if err != nil {
		t.Fatalf("create short-path Mac API fixture root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(apiRoot); err != nil {
			t.Errorf("remove short-path Mac API fixture root: %v", err)
		}
	})
	if err := os.Chmod(apiRoot, 0o700); err != nil {
		t.Fatalf("set Mac API fixture root to owner-only mode: %v", err)
	}
	runDir := filepath.Join(apiRoot, "run")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	metricsImporter, err := mailbox.NewImporter(filepath.Join(apiRoot, "mailbox"), nil)
	if err != nil {
		t.Fatal(err)
	}
	metricsRecorder := opshealth.NewRecorder()
	thresholds := opshealth.NewThresholdMonitor()
	api, err := localapi.NewServer(localapi.ServerOptions{
		Authority: authority, Owner: owner, SocketPath: filepath.Join(runDir, "api.sock"),
		HealthReport: func(ctx context.Context) opshealth.Report {
			return macIngressHealthReportWithMetrics(ctx, authority, monitor, metricsImporter, metricsRecorder, thresholds)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := api.Listen(); err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- api.Serve() }()
	t.Cleanup(func() {
		if err := api.Close(context.Background()); err != nil {
			t.Errorf("close isolated Mac API: %v", err)
		}
		if err := <-serveErr; err != nil {
			t.Errorf("isolated Mac API serve: %v", err)
		}
	})
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", api.SocketPath())
	}}}
	acceptedSessionIDs := make(map[string]string, 2)
	for _, target := range []struct {
		name        string
		environment string
		kind        string
		profile     string
	}{
		{name: "local", environment: "mac-dev", kind: "local", profile: "mac-workstation"},
		{name: "queued-remote", environment: "linux-dev", kind: "remote", profile: "linux-host"},
	} {
		body, err := json.Marshal(map[string]any{
			"environment":      target.environment,
			"execution_target": map[string]string{"kind": target.kind, "profile": target.profile},
			"source":           map[string]string{"mode": "empty"},
		})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://runner/v1/sessions", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Idempotency-Key", "p128-host-"+target.name)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("accept %s intent during remote outage: %v", target.name, err)
		}
		var accepted struct {
			SessionID       string `json:"session_id"`
			IntentID        string `json:"intent_id"`
			AcceptanceScope string `json:"acceptance_scope"`
			KnownState      struct {
				DeliveryState string `json:"delivery_state"`
			} `json:"known_state"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&accepted)
		closeErr := response.Body.Close()
		if decodeErr != nil || closeErr != nil || response.StatusCode != http.StatusAccepted || accepted.SessionID == "" || accepted.IntentID == "" || accepted.AcceptanceScope != "local_intent" || accepted.KnownState.DeliveryState != string(store.LocalIntentRecorded) {
			t.Fatalf("%s intent acceptance status=%d body=%+v decode=%v close=%v", target.name, response.StatusCode, accepted, decodeErr, closeErr)
		}
		intent, err := authority.GetLocalIntentByResource(ctx, "create_session", accepted.SessionID, owner)
		if err != nil || intent.IntentID != domain.IntentID(accepted.IntentID) || intent.DeliveryState != store.LocalIntentRecorded {
			t.Fatalf("%s intent not durable: intent=%+v err=%v", target.name, intent, err)
		}
		acceptedSessionIDs[target.name] = accepted.SessionID
	}
	healthResponse, err := client.Get("http://runner/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	var health opshealth.Report
	decodeErr := json.NewDecoder(healthResponse.Body).Decode(&health)
	closeErr := healthResponse.Body.Close()
	if decodeErr != nil || closeErr != nil || healthResponse.StatusCode != http.StatusOK || health.Readiness != opshealth.StateReady || health.Checks[1].State != opshealth.StateDegraded || health.Checks[1].Details["pending_intents"] != 1 || health.Metrics == nil || health.Metrics.QueuedIntents < 2 {
		t.Fatalf("Mac readiness during SSH outage status=%d report=%+v decode=%v close=%v", healthResponse.StatusCode, health, decodeErr, closeErr)
	}
	metricsResponse, err := client.Get("http://runner/metrics")
	if err != nil {
		t.Fatal(err)
	}
	var ingressMetrics opshealth.Metrics
	decodeErr = json.NewDecoder(metricsResponse.Body).Decode(&ingressMetrics)
	closeErr = metricsResponse.Body.Close()
	if decodeErr != nil || closeErr != nil || metricsResponse.StatusCode != http.StatusOK || ingressMetrics.QueuedIntents < 2 {
		t.Fatalf("Mac /metrics status=%d metrics=%+v decode=%v close=%v", metricsResponse.StatusCode, ingressMetrics, decodeErr, closeErr)
	}
	caller.setUnavailable(false)
	recovered := service.Doctor(ctx)
	if recovered.Readiness != opshealth.StateReady || len(recovered.Checks) != 2 || recovered.Checks[1].State != opshealth.StateReady {
		t.Fatalf("Mac Router health did not recover after restoring the real pinned SSH path: %+v", recovered)
	}
	for _, name := range []string{"local", "queued-remote"} {
		intent, err := authority.GetLocalIntentByResource(ctx, "create_session", acceptedSessionIDs[name], owner)
		if err != nil || intent.DeliveryState != store.LocalIntentRecorded {
			t.Fatalf("%s intent did not remain durably recorded after SSH recovery: intent=%+v err=%v", name, intent, err)
		}
	}
	t.Log("P128 Mac host gate: real pinned Ubuntu SSH ping passed, injected outage degraded Router while both intents committed durably, and restoring the real SSH path returned Router health to ready")
	p128CheckMacSQLiteWriteFailure(t)
}

func p128CheckMacIngressDoctor(t *testing.T, configPath string) {
	t.Helper()
	if configPath == "" || !filepath.IsAbs(configPath) {
		t.Fatal("RUNNER_P128_MAC_CONFIG must name the owner-only temporary Mac host configuration")
	}
	var output, stderr bytes.Buffer
	if code := runDoctor([]string{"--config", configPath}, &output, &stderr); code != 0 {
		t.Fatalf("runner-local doctor exit=%d stderr=%q report=%s", code, stderr.String(), output.String())
	}
	var report opshealth.Report
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("decode runner-local doctor report: %v; output=%s", err, output.String())
	}
	if report.Component != "mac_ingress" || report.Readiness != opshealth.StateReady || len(report.Checks) != 2 || report.Checks[0].Component != "sqlite_writes" || report.Checks[0].State != opshealth.StateReady || report.Checks[1].Component != "remote_router" || report.Checks[1].State != opshealth.StateReady {
		t.Fatalf("runner-local doctor did not report healthy ingress and Router: %+v", report)
	}
	if report.Metrics == nil {
		t.Fatal("runner-local doctor omitted operational metrics")
	}
	if strings.Contains(output.String(), "dispatcher_ed25519") || strings.Contains(output.String(), "direct-client.key") || strings.Contains(output.String(), "PRIVATE KEY") {
		t.Fatal("runner-local doctor report exposed a secret path or private-key marker")
	}

	databasePath := filepath.Join(config.MacServiceRoot, "state", "local.db")
	if err := os.Chmod(databasePath, 0o400); err != nil {
		t.Fatalf("make isolated Mac doctor database read-only: %v", err)
	}
	defer func() {
		if err := os.Chmod(databasePath, 0o600); err != nil {
			t.Errorf("restore isolated Mac doctor database mode: %v", err)
		}
	}()
	output.Reset()
	stderr.Reset()
	if code := runDoctor([]string{"--config", configPath}, &output, &stderr); code != 1 {
		t.Fatalf("runner-local doctor with read-only isolated DB exit=%d stderr=%q report=%s", code, stderr.String(), output.String())
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("decode read-only Mac doctor report: %v; output=%s", err, output.String())
	}
	if report.Readiness != opshealth.StateNotReady || len(report.Checks) != 1 || report.Checks[0].Component != "sqlite_writes" || report.Checks[0].Reason != "database_migration_or_write_failed" {
		t.Fatalf("runner-local doctor did not classify the isolated DB failure: %+v", report)
	}
	if strings.Contains(output.String(), "direct-client.key") || strings.Contains(output.String(), "PRIVATE KEY") {
		t.Fatal("failed Mac doctor report exposed credential information")
	}
	t.Log("runner-local doctor reports healthy ingress separately from remote Router and classifies an isolated SQLite write failure without credential details")
}

func p128CheckMacSQLiteWriteFailure(t *testing.T) {
	t.Helper()
	root := testfixture.New(t)
	db, err := store.Open(context.Background(), filepath.Join(root.Path(), "state", "p128-host-read-only.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.CheckWritable(context.Background(), "mac_ingress_host_gate"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA query_only = ON"); err != nil {
		t.Fatal(err)
	}
	report := macIngressHealthReport(context.Background(), authority, newRouterHealthMonitor())
	if report.Readiness != opshealth.StateNotReady || report.Checks[0].Reason != "database_write_failed" {
		t.Fatalf("Mac health did not mark ingress unready after an isolated SQLite write failure: %+v", report)
	}
}

type p128SwitchableSSHCaller struct {
	mu          sync.RWMutex
	client      *sshclient.Client
	unavailable bool
}

func (c *p128SwitchableSSHCaller) setUnavailable(value bool) {
	c.mu.Lock()
	c.unavailable = value
	c.mu.Unlock()
}

func (c *p128SwitchableSSHCaller) unavailableNow() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.unavailable
}

func (c *p128SwitchableSSHCaller) Call(ctx context.Context, request sshbridge.RequestFrame) (sshbridge.ReplyFrame, error) {
	if c.unavailableNow() {
		return sshbridge.ReplyFrame{}, &sshclient.TransportError{Phase: sshclient.PhaseBeforeSend, Err: errors.New("P128 simulated SSH outage")}
	}
	return c.client.Call(ctx, request)
}
