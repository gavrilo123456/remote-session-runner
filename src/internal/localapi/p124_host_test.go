//go:build p124twohost

package localapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/runnercli"
	"remote-session-runner/src/internal/runnerlocald"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/sshclient"
	"remote-session-runner/src/internal/store"
)

type p124CLIResult struct {
	code   int
	stdout string
	stderr string
}

func TestP124CommonCLISmokeAcrossRoutes(t *testing.T) {
	if os.Getenv("RSR_P124_HOST_GATE") != "1" {
		t.Skip("set RSR_P124_HOST_GATE=1 to run the real Mac/Ubuntu P-CLI-01 gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P124 CLI must run on the selected Mac, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != config.MacAccount {
		t.Fatalf("P124 CLI account=%v err=%v, want %s", current, err, config.MacAccount)
	}
	t.Logf("machine=Mac account=%s uid=%s os=%s go=%s direct=%s queued=ubuntu@129.151.232.40",
		current.Username, current.Uid, runtime.GOOS, runtime.Version(), config.PublicEndpoint)

	serverCA := p124RequiredFixturePath(t, "RUNNER_P124_SERVER_CA")
	clientCert := p124RequiredFixturePath(t, "RUNNER_P124_CLIENT_CERT")
	clientKey := p124RequiredFixturePath(t, "RUNNER_P124_CLIENT_KEY")
	sshIdentity := p124RequiredFixturePath(t, "RUNNER_P124_SSH_IDENTITY")
	knownHosts := p124RequiredFixturePath(t, "RUNNER_P124_SSH_KNOWN_HOSTS")
	for name, path := range map[string]string{
		"RUNNER_P124_SERVER_CA": serverCA, "RUNNER_P124_CLIENT_CERT": clientCert,
		"RUNNER_P124_CLIENT_KEY": clientKey, "RUNNER_P124_SSH_IDENTITY": sshIdentity,
		"RUNNER_P124_SSH_KNOWN_HOSTS": knownHosts,
	} {
		want := map[string]string{
			"RUNNER_P124_SERVER_CA":       filepath.Join(config.MacServiceRoot, "secrets", "poc-ca.pem"),
			"RUNNER_P124_CLIENT_CERT":     filepath.Join(config.MacServiceRoot, "secrets", "direct-client.pem"),
			"RUNNER_P124_CLIENT_KEY":      filepath.Join(config.MacServiceRoot, "secrets", "direct-client.key"),
			"RUNNER_P124_SSH_IDENTITY":    filepath.Join(config.MacServiceRoot, "secrets", "dispatcher_ed25519"),
			"RUNNER_P124_SSH_KNOWN_HOSTS": filepath.Join(config.MacServiceRoot, "secrets", "ssh_known_hosts"),
		}[name]
		if path != want {
			t.Fatalf("%s points to %q, want selected host file %q", name, path, want)
		}
	}
	macConfigPath := p124WriteMacConfig(t, serverCA, clientCert, clientKey, sshIdentity, knownHosts)

	h := newP095Harness(t)
	owner := p063Owner(t)
	localAPISocket, removeRunDir := p124PrepareLocalAPISocket(t)
	localAPI, err := NewServer(ServerOptions{Authority: h.authority, Owner: owner, SocketPath: localAPISocket})
	if err != nil {
		t.Fatal(err)
	}
	if err := localAPI.Listen(); err != nil {
		t.Fatal(err)
	}
	localAPIServeErr := make(chan error, 1)
	go func() { localAPIServeErr <- localAPI.Serve() }()
	localAPIClosed := false
	closeLocalAPI := func() error {
		if localAPIClosed {
			return nil
		}
		localAPIClosed = true
		closeErr := localAPI.Close(context.Background())
		serveErr := <-localAPIServeErr
		return errors.Join(closeErr, serveErr)
	}
	t.Cleanup(func() {
		if !localAPIClosed {
			if err := closeLocalAPI(); err != nil {
				t.Errorf("close temporary Mac local API: %v", err)
			}
		}
		removeRunDir()
	})

	localTarget, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	localEnvironment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "mac-dev", HostClass: "macOS workstation", EffectiveAccount: config.MacAccount,
		AllowedTargets:     []domain.ExecutionTarget{localTarget},
		AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{owner},
		ServiceLimits:      domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	localWorkspace := filepath.Join(t.TempDir(), "workspaces")
	if err := os.Mkdir(localWorkspace, 0o700); err != nil {
		t.Fatal(err)
	}
	localService, _, err := runnerlocald.NewMacExecutionService(h.authority, hostruntime.MacRuntimeOptions{
		Account: config.MacAccount, WorkspaceRoot: localWorkspace, ShellPath: "/bin/bash",
	}, localEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	localdSocket := filepath.Join(filepath.Dir(localAPISocket), "locald.sock")
	locald, err := runnerlocald.NewPrivateServer(runnerlocald.PrivateServerOptions{
		Authority: h.authority, Service: localService, Owner: owner, SocketPath: localdSocket,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := locald.Listen(); err != nil {
		t.Fatal(err)
	}
	localdServeErr := make(chan error, 1)
	go func() { localdServeErr <- locald.Serve() }()
	t.Cleanup(func() {
		if err := locald.Close(context.Background()); err != nil {
			t.Errorf("close temporary Mac locald: %v", err)
		}
		if err := <-localdServeErr; err != nil {
			t.Errorf("serve temporary Mac locald: %v", err)
		}
	})
	localdClient, err := dispatcher.NewLocaldClient(localdSocket)
	if err != nil {
		t.Fatal(err)
	}
	localDriver, err := dispatcher.NewLocalDriver(h.authority, localdClient, "router-p124-cli", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ssh, err := sshclient.New(sshclient.Config{
		User: "ubuntu", Host: "129.151.232.40", IdentityFile: sshIdentity, KnownHostsFile: knownHosts,
	})
	if err != nil {
		t.Fatal(err)
	}
	remoteDriver, err := dispatcher.NewRemoteDriver(h.authority, ssh, "router-p124-cli", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	queuedController, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, domain.ControllerID(config.MacAccount))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	localCreate := p124RunDelayedCreate(t, ctx, h.authority, localDriver, remoteDriver, owner, queuedController,
		"mac-dev", "local", "mac-workstation", "p124-local-create")
	localSessionID := p124RequireField(t, localCreate.stdout, "session_id")
	if localCreate.code != 0 || !strings.Contains(localCreate.stdout, "session_state: ready") {
		t.Fatalf("local delayed create result=%+v; want successful ready result", localCreate)
	}

	queuedCreate, queuedSessionID := p124CreateWithoutWait(t, ctx, h.authority, localDriver, remoteDriver, owner, queuedController,
		"local", "linux-dev", "remote", "linux-host", "p124-queued-create")
	if queuedCreate.code != 0 || !strings.Contains(queuedCreate.stdout, "readiness: pending (not waited") {
		t.Fatalf("queued --no-wait result=%+v; want accepted pending result", queuedCreate)
	}
	directCreate := p124RunCLI("linux-poc", macConfigPath, "session", "create", "--no-wait",
		"--environment", "linux-dev", "--target", "remote", "--profile", "linux-host",
		"--idempotency-key", "p124-direct-create")
	directSessionID := p124RequireField(t, directCreate.stdout, "session_id")
	if directCreate.code != 0 || !strings.Contains(directCreate.stdout, "readiness: pending (not waited") {
		t.Fatalf("direct --no-wait result=%+v; want accepted pending result", directCreate)
	}

	routes := []struct {
		name, endpoint, configPath, sessionID, target, profile, account string
		queued                                                          bool
	}{
		{"local", "local", "", localSessionID, "local", "mac-workstation", config.MacAccount, false},
		{"queued-remote", "local", "", queuedSessionID, "remote", "linux-host", "ubuntu", true},
		{"direct-remote", "linux-poc", macConfigPath, directSessionID, "remote", "linux-host", "ubuntu", false},
	}
	for _, route := range routes {
		status := p124WaitReadyStatus(t, route.endpoint, route.configPath, route.sessionID)
		if !strings.Contains(status.stdout, "execution_target: "+route.target+"/"+route.profile) ||
			!strings.Contains(status.stdout, "effective_account: "+route.account) ||
			!strings.Contains(status.stdout, "isolation: os-user") {
			t.Fatalf("%s status does not truthfully report target/account/isolation: %q", route.name, status.stdout)
		}

		marker := "P124-" + strings.ToUpper(strings.ReplaceAll(route.name, "-", "_")) + "-OUTPUT"
		// Exit from a child Bash, so the target command is nonzero while the
		// persistent session shell remains available for status and close.
		script := fmt.Sprintf("bash -c 'printf \"%s\\n\"; exit 7'", marker)
		var executed p124CLIResult
		if route.queued || route.target == "local" {
			executed = p124RunIntentCLI(t, h.authority, localDriver, remoteDriver, owner, queuedController,
				route.endpoint, route.configPath,
				[]string{"exec", "--idempotency-key", "p124-" + route.name + "-exec", route.sessionID, "--", script},
				"submit_command", route.target)
		} else {
			executed = p124RunCLI(route.endpoint, route.configPath, "exec",
				"--idempotency-key", "p124-direct-remote-exec", route.sessionID, "--", script)
		}
		commandID := p124RequireField(t, executed.stderr, "command_id")
		if executed.code != 7 || !strings.Contains(executed.stdout, marker) {
			t.Fatalf("%s exec result=%+v; want raw output and child exit code 7", route.name, executed)
		}
		events := p124RunCLI(route.endpoint, route.configPath, "events", commandID, "--after", "0")
		if events.code != 0 || !strings.Contains(events.stdout, marker) ||
			!strings.Contains(events.stderr, "event_history_complete_this_read: true") {
			t.Fatalf("%s events result=%+v; want complete replay including output", route.name, events)
		}

		var closed p124CLIResult
		if route.queued || route.target == "local" {
			closed = p124RunIntentCLI(t, h.authority, localDriver, remoteDriver, owner, queuedController,
				route.endpoint, route.configPath,
				[]string{"session", "close", "--idempotency-key", "p124-" + route.name + "-close", route.sessionID},
				"close_session", route.target)
		} else {
			closed = p124RunCLI(route.endpoint, route.configPath, "session", "close",
				"--idempotency-key", "p124-direct-remote-close", route.sessionID)
		}
		if closed.code != 0 || !strings.Contains(closed.stdout, "session_state: closed") {
			t.Fatalf("%s close result=%+v; want confirmed closed state", route.name, closed)
		}
	}

	wrongEndpoint := p124RunCLI("local", "", "session", "status", directSessionID)
	if wrongEndpoint.code == 0 || !strings.Contains(wrongEndpoint.stderr, "session status request failed") ||
		strings.Contains(wrongEndpoint.stdout, "endpoint: linux-poc") {
		t.Fatalf("direct session ID was unexpectedly routed away from explicit local endpoint: %+v", wrongEndpoint)
	}
	if err := closeLocalAPI(); err != nil {
		t.Fatalf("stop local API for transport-failure check: %v", err)
	}
	transportFailure := p124RunCLI("local", "", "session", "status", localSessionID)
	if transportFailure.code != 1 || !strings.Contains(transportFailure.stderr, "session status request failed") ||
		!strings.Contains(transportFailure.stderr, "transport") {
		t.Fatalf("transport failure result=%+v; want distinct CLI transport error", transportFailure)
	}
	t.Log("P-CLI-01 PASS: delayed readiness, --no-wait, local/queued/direct create-status-exec-events-close, explicit endpoint, truthful target/account/isolation, child exit 7, replay, and distinct transport error")
}

func p124RunDelayedCreate(t *testing.T, ctx context.Context, authority *store.AuthorityStore,
	local *dispatcher.LocalDriver, remote *dispatcher.RemoteDriver, owner, queued domain.ControllerIdentity,
	environment, target, profile, key string) p124CLIResult {
	t.Helper()
	done := p124StartCLI("local", "", "--wait-timeout=45s", "session", "create",
		"--environment", environment, "--target", target, "--profile", profile, "--idempotency-key", key)
	intent := p124WaitEligibleIntent(t, ctx, authority, time.Now().Add(-time.Second), "create_session", target)
	time.Sleep(700 * time.Millisecond)
	select {
	case early := <-done:
		t.Fatalf("default create returned before delayed target dispatch: %+v", early)
	default:
	}
	if err := p124DispatchIntent(t, ctx, authority, local, remote, owner, queued, intent); err != nil {
		t.Fatal(err)
	}
	return p124AwaitCLI(t, done)
}

func p124CreateWithoutWait(t *testing.T, ctx context.Context, authority *store.AuthorityStore,
	local *dispatcher.LocalDriver, remote *dispatcher.RemoteDriver, owner, queued domain.ControllerIdentity,
	endpoint, environment, target, profile, key string) (p124CLIResult, string) {
	t.Helper()
	result := p124RunCLI(endpoint, "", "session", "create", "--no-wait",
		"--environment", environment, "--target", target, "--profile", profile, "--idempotency-key", key)
	sessionID := p124RequireField(t, result.stdout, "session_id")
	intent, err := authority.GetLocalIntentByResource(ctx, "create_session", sessionID, owner)
	if err != nil {
		t.Fatalf("look up accepted queued session intent: %v", err)
	}
	if err := p124DispatchIntent(t, ctx, authority, local, remote, owner, queued, intent); err != nil {
		t.Fatal(err)
	}
	return result, sessionID
}

func p124RunIntentCLI(t *testing.T, authority *store.AuthorityStore,
	local *dispatcher.LocalDriver, remote *dispatcher.RemoteDriver, owner, queued domain.ControllerIdentity,
	endpoint, configPath string, command []string, operation, target string) p124CLIResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	done := p124StartCLI(endpoint, configPath, append([]string{"--wait-timeout=45s"}, command...)...)
	intent := p124WaitEligibleIntent(t, ctx, authority, time.Now().Add(-time.Second), operation, target)
	if err := p124DispatchIntent(t, ctx, authority, local, remote, owner, queued, intent); err != nil {
		t.Fatal(err)
	}
	return p124AwaitCLI(t, done)
}

func p124DispatchIntent(t *testing.T, ctx context.Context, authority *store.AuthorityStore,
	local *dispatcher.LocalDriver, remote *dispatcher.RemoteDriver, owner, queued domain.ControllerIdentity,
	intent store.LocalIntentRecord) error {
	t.Helper()
	if intent.Target.Kind() == domain.TargetKindLocal {
		_, _, err := local.DispatchIntent(ctx, intent.IntentID)
		return err
	}
	if intent.Target.Kind() != domain.TargetKindRemote {
		return fmt.Errorf("unsupported P124 execution target %q", intent.Target.Kind())
	}
	accepted, _, err := remote.DispatchIntent(ctx, intent.IntentID)
	if err != nil {
		return err
	}
	if accepted.Operation != "submit_command" {
		return nil
	}
	commandID := accepted.CommandID
	if commandID == "" {
		commandID = intent.CommandID
	}
	if commandID == "" {
		return errors.New("queued CLI command acceptance has no command ID")
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := remote.MirrorCommandEvents(ctx, commandID, queued); err != nil {
			return fmt.Errorf("mirror queued CLI command events: %w", err)
		}
		projection, err := remote.RefreshCommandProjection(ctx, commandID, owner)
		if err != nil {
			return fmt.Errorf("refresh queued CLI command projection: %w", err)
		}
		if projection.State.IsTerminal() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("queued CLI command %s did not reach a terminal state: %w", commandID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func p124WaitEligibleIntent(t *testing.T, ctx context.Context, authority *store.AuthorityStore,
	after time.Time, operation, target string) store.LocalIntentRecord {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		intents, err := authority.ListEligibleLocalIntents(ctx, 1000)
		if err != nil {
			t.Fatalf("list CLI intents: %v", err)
		}
		for _, intent := range intents {
			if intent.Operation == operation && string(intent.Target.Kind()) == target && intent.CreatedAt.After(after) {
				return intent
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for CLI %s intent targeting %s: %v", operation, target, ctx.Err())
		case <-ticker.C:
		}
	}
}

func p124WaitReadyStatus(t *testing.T, endpoint, configPath, sessionID string) p124CLIResult {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		result := p124RunCLI(endpoint, configPath, "session", "status", sessionID)
		if result.code == 0 && strings.Contains(result.stdout, "session_state: ready") {
			return result
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("session %s did not become ready through endpoint %s", sessionID, endpoint)
	return p124CLIResult{}
}

func p124StartCLI(endpoint, configPath string, command ...string) <-chan p124CLIResult {
	done := make(chan p124CLIResult, 1)
	go func() {
		args := []string{"--endpoint", endpoint}
		if configPath != "" {
			args = append(args, "--config", configPath)
		}
		args = append(args, command...)
		done <- p124InvokeCLI(args)
	}()
	return done
}

func p124RunCLI(endpoint, configPath string, command ...string) p124CLIResult {
	args := []string{"--endpoint", endpoint}
	if configPath != "" {
		args = append(args, "--config", configPath)
	}
	args = append(args, command...)
	return p124InvokeCLI(args)
}

func p124InvokeCLI(args []string) p124CLIResult {
	var stdout, stderr bytes.Buffer
	code := runnercli.Run(args, &stdout, &stderr)
	return p124CLIResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func p124AwaitCLI(t *testing.T, done <-chan p124CLIResult) p124CLIResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(90 * time.Second):
		t.Fatal("CLI command did not finish within 90 seconds")
		return p124CLIResult{}
	}
}

func p124RequireField(t *testing.T, output, name string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == name && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	t.Fatalf("CLI output has no %s field: %q", name, output)
	return ""
}

func p124RequiredFixturePath(t *testing.T, name string) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(name))
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatalf("%s must name an absolute selected fixture file", name)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		t.Fatalf("%s must name a small regular file: %v", name, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o400 == 0 {
		t.Fatalf("%s must be owner-readable and restricted to the current account", name)
	}
	return path
}

func p124WriteMacConfig(t *testing.T, serverCA, clientCert, clientKey, sshIdentity, knownHosts string) string {
	t.Helper()
	root := config.MacServiceRoot
	lines := []string{
		"version: 1", "mac:",
		fmt.Sprintf("  account: %s", config.MacAccount),
		fmt.Sprintf("  service_root: %q", root),
		fmt.Sprintf("  api_socket: %q", filepath.Join(root, "run", "local-api.sock")),
		fmt.Sprintf("  locald_socket: %q", filepath.Join(root, "run", "locald.sock")),
		fmt.Sprintf("  sqlite: %q", filepath.Join(root, "state", "local.db")),
		fmt.Sprintf("  mailbox_root: %q", filepath.Join(root, "mailbox")),
		fmt.Sprintf("  workspaces: %q", filepath.Join(root, "workspaces")),
		fmt.Sprintf("  script_temp_root: %q", filepath.Join(root, "tmp", "scripts")),
		fmt.Sprintf("  backups: %q", filepath.Join(root, "backups")),
		fmt.Sprintf("  remote_endpoint_profile: %s", config.MacEndpointName),
		fmt.Sprintf("  remote_endpoint: %s", config.PublicEndpoint),
		fmt.Sprintf("  remote_server_ca_certificate: %q", serverCA),
		"  ssh_host_alias: remote-session-runner",
		fmt.Sprintf("  ssh_known_hosts: %q", knownHosts),
		fmt.Sprintf("  direct_client_certificate: %q", clientCert),
		"  reconciliation_deadline: 24h",
		"secret_references:", "  dispatcher_ssh_key:",
		fmt.Sprintf("    file: %q", sshIdentity), "  direct_client_private_key:",
		fmt.Sprintf("    file: %q", clientKey), "environment_registry:", "  mac-dev:",
		"    base_system: macOS", "    host_class: macOS workstation",
		fmt.Sprintf("    effective_account: %s", config.MacAccount),
		"    allowed_targets:", "      - kind: local", "        profile: mac-workstation",
		"    allowed_source_modes: [empty, local_worktree]", "    allowed_repository_aliases: []",
		"    allowed_controllers:", "      - type: local_user",
		fmt.Sprintf("        id: %s", config.MacAccount), "  linux-dev:",
		"    base_system: Ubuntu 20.04.6 LTS", "    host_class: Ubuntu Linux host",
		"    effective_account: ubuntu", "    allowed_targets:",
		"      - kind: remote", "        profile: linux-host", "    allowed_source_modes: [empty]",
		"    allowed_repository_aliases: []", "    allowed_controllers:",
		"      - type: queued_mac", fmt.Sprintf("        id: %s", config.MacAccount),
		"      - type: direct_mtls", fmt.Sprintf("        id: %s", config.MacAccount),
	}
	path := filepath.Join(t.TempDir(), "mac.yaml")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadFile(path); err != nil {
		t.Fatalf("load temporary selected Mac CLI profile: %v", err)
	}
	return path
}

func p124PrepareLocalAPISocket(t *testing.T) (string, func()) {
	t.Helper()
	runDir := filepath.Join(config.MacServiceRoot, "run")
	created := false
	info, err := os.Lstat(runDir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(runDir, 0o700); err != nil {
			t.Fatalf("create temporary Mac service run directory: %v", err)
		}
		created = true
	} else if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("selected Mac run directory is not a private real directory: info=%v err=%v", info, err)
	}
	socket := filepath.Join(runDir, "local-api.sock")
	if _, err := os.Lstat(socket); err == nil {
		t.Fatalf("refusing to replace existing local API path %s", socket)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect local API socket path: %v", err)
	}
	localdSocket := filepath.Join(runDir, "locald.sock")
	if _, err := os.Lstat(localdSocket); err == nil {
		t.Fatalf("refusing to replace existing locald path %s", localdSocket)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect locald socket path: %v", err)
	}
	return socket, func() {
		if created {
			_ = os.Remove(runDir)
		}
	}
}
