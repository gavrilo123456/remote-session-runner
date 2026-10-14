package runnerlocal

import (
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
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailboxclient"
)

const (
	p157FactoryMailboxID       = "agentic-software-factory-runner"
	p157FactoryMailboxRoot     = "/Users/tomasz.walczuk/projects/agentic-software-factory/runner/tmp/mailbox-"
	p157FactoryRepositoryAlias = "agentic-software-factory"
	p157FactoryContextName     = "ubuntu-factory-sandbox"
	p157FactoryEnvironment     = "factory-sandbox-dev"
	p157FactoryRemoteProfile   = "factory-sandbox"
	p157FactoryBridgeHost      = "145.241.255.66"
	p157FactoryBridgePort      = 22
	p157FactoryDirectEndpoint  = "factory-sandbox-poc"
	p157FactoryDirectURL       = "https://145.241.255.66:8443"
	p157FactoryExpectedMarker  = "FACTORY_SANDBOX_MAILBOX_OK"
	p157FactoryExpectedOutput  = "FACTORY_SANDBOX_MAILBOX_OK\nubuntu\noracle-waclaw-dziwulski-ubuntu-micro-01\nx86_64\n"
	p157FactoryExpectedCursor  = int64(5)
)

// TestP157FactorySandboxExternalMailboxDefaultGate proves the installed
// agentic-software-factory mailbox uses its configured factory-sandbox
// default through a native marker-last exchange. It is opt-in because it
// submits one harmless request with both target fields intentionally omitted.
func TestP157FactorySandboxExternalMailboxDefaultGate(t *testing.T) {
	if os.Getenv("RSR_P157_FACTORY_SANDBOX_HOST_GATE") != "1" {
		t.Skip("set RSR_P157_FACTORY_SANDBOX_HOST_GATE=1 to run the factory-sandbox external-mailbox gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P157 factory-sandbox gate must run on the selected Mac, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != config.MacAccount {
		t.Fatalf("P157 factory-sandbox account=%v err=%v, want %s", current, err, config.MacAccount)
	}

	loaded, err := config.LoadFile(p155MacConfigPath)
	if err != nil {
		t.Fatalf("load installed config: %v", err)
	}
	if loaded.SchemaVersion() != config.VersionV2 {
		t.Fatalf("installed schema version=%d, want %d", loaded.SchemaVersion(), config.VersionV2)
	}
	mailbox, ok := loaded.Mailbox(p157FactoryMailboxID)
	if !ok || mailbox.Root != p157FactoryMailboxRoot || mailbox.DefaultExecution != p157FactoryContextName ||
		mailbox.DurableOrphanCleanup || !p155Contains(mailbox.RepositoryAliases, p157FactoryRepositoryAlias) ||
		!p155Contains(mailbox.AllowedExecution, p157FactoryContextName) || !p155Contains(mailbox.AllowedExecution, "mac-local") {
		t.Fatalf("installed factory-sandbox mailbox policy=%+v present=%t", mailbox, ok)
	}
	context, ok := loaded.ExecutionContext(p157FactoryContextName)
	if !ok || context.Environment != p157FactoryEnvironment || context.Target.Kind() != domain.TargetKindRemote || context.Target.Profile() != p157FactoryRemoteProfile {
		t.Fatalf("factory-sandbox default context=%+v present=%t", context, ok)
	}
	selection, err := loaded.ResolveMailboxExecution(p157FactoryMailboxID, false, "", false, domain.ExecutionTarget{}, p157FactoryRepositoryAlias)
	if err != nil || selection.MailboxID != p157FactoryMailboxID || selection.ContextName != p157FactoryContextName ||
		selection.Environment != p157FactoryEnvironment || selection.Target.Kind() != domain.TargetKindRemote ||
		selection.Target.Profile() != p157FactoryRemoteProfile || selection.Source != config.MailboxExecutionSelectionSourceInboxDefault ||
		selection.RepositoryAlias != p157FactoryRepositoryAlias {
		t.Fatalf("factory-sandbox omitted-target selection=%+v err=%v", selection, err)
	}

	factoryHost, ok := loaded.RemoteHost(p157FactoryRemoteProfile)
	if !ok || factoryHost.Account != config.LinuxAccount || factoryHost.QueuedBridge == nil || factoryHost.DirectEndpoint == nil ||
		factoryHost.QueuedBridge.Host != p157FactoryBridgeHost || factoryHost.QueuedBridge.Port != p157FactoryBridgePort ||
		factoryHost.DirectEndpoint.Name != p157FactoryDirectEndpoint || factoryHost.DirectEndpoint.TargetProfile != p157FactoryRemoteProfile ||
		factoryHost.DirectEndpoint.Endpoint != p157FactoryDirectURL {
		t.Fatalf("factory-sandbox remote profile=%+v present=%t", factoryHost, ok)
	}
	configuredDirect, ok := loaded.DirectEndpoint(p157FactoryDirectEndpoint)
	if !ok || configuredDirect.TargetProfile != p157FactoryRemoteProfile || configuredDirect.Endpoint != p157FactoryDirectURL {
		t.Fatalf("factory-sandbox direct endpoint=%+v present=%t", configuredDirect, ok)
	}
	for _, existingProfile := range []string{"linux-host", "sandbox-host"} {
		existingHost, ok := loaded.RemoteHost(existingProfile)
		if !ok || existingHost.QueuedBridge == nil || existingHost.DirectEndpoint == nil {
			t.Fatalf("%s profile is incomplete: %+v present=%t", existingProfile, existingHost, ok)
		}
		p157FactoryRequireIndependentMaterialPaths(t, factoryHost, existingProfile, existingHost)
	}

	p155WaitForLoadedLaunchAgent(t, "com.remote-session-runner.local")
	p155WaitForLoadedLaunchAgent(t, "com.remote-session-runner.locald")
	localSocket := filepath.Join(p155MacServiceRoot, "run", "local-api.sock")
	p155WaitForOwnedSocket(t, localSocket)
	p157FactoryRequireSafeExternalMailboxTree(t, mailbox.Root)
	p157FactoryWaitForRouterAndMailboxQuiescence(t, localSocket)

	client, err := mailboxclient.New(mailbox.Root)
	if err != nil {
		t.Fatalf("open factory-sandbox mailbox client: %v", err)
	}
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	requestID := "req-p157-factory-sandbox-" + suffix
	request := p155Request(t, map[string]any{
		"request_id": requestID, "idempotency_key": "key-p157-factory-sandbox-" + suffix,
		"operation": "run", "repository_alias": p157FactoryRepositoryAlias,
		"script": "printf 'FACTORY_SANDBOX_MAILBOX_OK\\n'; id -un; hostname; uname -m",
		// environment and execution_target are deliberately omitted.
	})
	if err := client.WriteRequest(requestID, request); err != nil {
		t.Fatalf("publish factory-sandbox request: %v", err)
	}
	response := p155WaitTerminalResponse(t, client, requestID)
	p155AssertRunResponse(t, response, p157FactoryMailboxID, config.MailboxExecutionSelectionSourceInboxDefault,
		p157FactoryEnvironment, "remote", p157FactoryRemoteProfile, p157FactoryExpectedOutput)
	if response.Stdout != p157FactoryExpectedOutput {
		t.Fatalf("factory-sandbox stdout=%q, want %q", response.Stdout, p157FactoryExpectedOutput)
	}
	if response.FinalEventSequence == nil || response.AvailableEventSequence == nil ||
		*response.FinalEventSequence != p157FactoryExpectedCursor ||
		*response.AvailableEventSequence != p157FactoryExpectedCursor {
		t.Fatalf("factory-sandbox terminal event cursor=%v/%v, want %d", response.FinalEventSequence, response.AvailableEventSequence, p157FactoryExpectedCursor)
	}
	p157FactoryAssertExactEvents(t, client, response)
	if err := client.WriteAcknowledgment(requestID, response); err != nil {
		t.Fatalf("publish factory-sandbox ACK: %v", err)
	}
	p155WaitAckConsumed(t, mailbox.Root, requestID)
	p155WaitRequestConsumed(t, mailbox.Root, requestID)
	p157FactoryWaitForRouterAndMailboxQuiescence(t, localSocket)
	t.Logf("P157 factory-sandbox complete: request_id=%s command=%s marker=%s", requestID, response.CommandID, p157FactoryExpectedMarker)
}

func p157FactoryRequireIndependentMaterialPaths(t *testing.T, factoryHost config.RemoteHostProfile, existingProfile string, existingHost config.RemoteHostProfile) {
	t.Helper()
	for _, path := range []struct {
		name     string
		factory  string
		existing string
	}{
		{name: "queued known-hosts", factory: factoryHost.QueuedBridge.KnownHostsFile, existing: existingHost.QueuedBridge.KnownHostsFile},
		{name: "queued private key", factory: factoryHost.QueuedBridge.PrivateKeyFile, existing: existingHost.QueuedBridge.PrivateKeyFile},
		{name: "direct server CA", factory: factoryHost.DirectEndpoint.ServerCA, existing: existingHost.DirectEndpoint.ServerCA},
		{name: "direct client certificate", factory: factoryHost.DirectEndpoint.ClientCertificate, existing: existingHost.DirectEndpoint.ClientCertificate},
		{name: "direct client private key", factory: factoryHost.DirectEndpoint.ClientPrivateKeyFile, existing: existingHost.DirectEndpoint.ClientPrivateKeyFile},
	} {
		if path.factory == "" || path.existing == "" || path.factory == path.existing {
			t.Fatalf("factory-sandbox %s is not an independent configured path: factory=%q %s=%q", path.name, path.factory, existingProfile, path.existing)
		}
	}
}

func p157FactoryRequireSafeExternalMailboxTree(t *testing.T, root string) {
	t.Helper()
	parent := filepath.Dir(root)
	trustedRoot := string(filepath.Separator)
	relative, err := filepath.Rel(trustedRoot, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("factory-sandbox mailbox parent is not below filesystem root: %q", parent)
	}
	current := trustedRoot
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
			t.Fatalf("unsafe factory-sandbox mailbox ancestor %s: info=%v err=%v", current, info, err)
		}
		if current == parent {
			p157FactoryRequireOwned0700Directory(t, current)
		}
	}
	for _, child := range []string{"", "inbox", "outbox", "events", "acks", "diagnostics"} {
		path := root
		if child != "" {
			path = filepath.Join(root, child)
		}
		p157FactoryRequireOwned0700Directory(t, path)
	}
}

func p157FactoryRequireOwned0700Directory(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		t.Fatalf("factory-sandbox mailbox directory %s must be an owned 0700 directory: info=%v err=%v", path, info, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		t.Fatalf("factory-sandbox mailbox directory %s is not owned by uid %d", path, os.Geteuid())
	}
}

func p157FactoryWaitForRouterAndMailboxQuiescence(t *testing.T, socketPath string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var last any
	for {
		report, err := p155ReadHealth(socketPath)
		if err == nil && p157FactoryMacZeroWork(report) &&
			p155HealthCheckIs(report, "remote_router/"+p157FactoryRemoteProfile, "ready") {
			return
		}
		if err != nil {
			last = err
		} else {
			last = report
		}
		if time.Now().After(deadline) {
			t.Fatalf("factory-sandbox route did not become ready with zero mailbox backlog: %v", last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func p157FactoryMacZeroWork(report p155HealthReport) bool {
	return report.Readiness == "ready" &&
		report.Metrics.ActiveSessionSlots == 0 &&
		report.Metrics.ActiveCommandSlots == 0 &&
		report.Metrics.QueuedCommands == 0 &&
		report.Metrics.QueuedIntents == 0 &&
		report.Metrics.MailboxBacklog == 0
}

func TestP157FactoryMacZeroWork(t *testing.T) {
	report := p155HealthReport{Readiness: "ready"}
	if !p157FactoryMacZeroWork(report) {
		t.Fatal("zeroed ready report must be quiescent")
	}
	for name, set := range map[string]func(*p155HealthReport){
		"not ready":       func(report *p155HealthReport) { report.Readiness = "degraded" },
		"active session":  func(report *p155HealthReport) { report.Metrics.ActiveSessionSlots = 1 },
		"active command":  func(report *p155HealthReport) { report.Metrics.ActiveCommandSlots = 1 },
		"queued command":  func(report *p155HealthReport) { report.Metrics.QueuedCommands = 1 },
		"queued intent":   func(report *p155HealthReport) { report.Metrics.QueuedIntents = 1 },
		"mailbox backlog": func(report *p155HealthReport) { report.Metrics.MailboxBacklog = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			report := p155HealthReport{Readiness: "ready"}
			set(&report)
			if p157FactoryMacZeroWork(report) {
				t.Fatalf("report=%+v must not be quiescent", report)
			}
		})
	}
}

func p157FactoryAssertExactEvents(t *testing.T, client *mailboxclient.Client, response mailboxclient.Response) {
	t.Helper()
	events, err := client.ReadEventsThroughCursor(response)
	if err != nil {
		t.Fatalf("read factory-sandbox events: %v", err)
	}
	var output strings.Builder
	for _, event := range events {
		if event.Type == "stdout" {
			if event.Encoding != "utf8" {
				t.Fatalf("factory-sandbox stdout encoding=%q", event.Encoding)
			}
			output.WriteString(event.Text)
		}
	}
	if output.String() != p157FactoryExpectedOutput {
		t.Fatalf("factory-sandbox event stdout=%q, want %q", output.String(), p157FactoryExpectedOutput)
	}
}
