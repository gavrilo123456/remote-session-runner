package runnerlocal

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailboxclient"
)

const (
	p157MailboxID      = "analytics"
	p157ContextName    = "ubuntu-sandbox"
	p157Environment    = "sandbox-dev"
	p157RemoteProfile  = "sandbox-host"
	p157BridgeHost     = "132.226.205.205"
	p157BridgePort     = 22
	p157DirectEndpoint = "sandbox-poc"
	p157DirectURL      = "https://132.226.205.205:8443"
	p157ExpectedHost   = "oracle-gustaw-janecki-ubuntu-flex-02"
	p157ExpectedArch   = "aarch64"
	p157ExpectedMarker = "P157_SANDBOX_OK"
)

// TestP157SandboxHostMailboxGate validates the installed Mac route to the
// separately provisioned ARM64 sandbox host. It is opt-in because it submits
// one harmless request through the native analytics mailbox client. It never
// writes a mailbox pair through shell redirection, deploys a service, or reads
// key material.
func TestP157SandboxHostMailboxGate(t *testing.T) {
	if os.Getenv("RSR_P157_SANDBOX_HOST_GATE") != "1" {
		t.Skip("set RSR_P157_SANDBOX_HOST_GATE=1 to run the sandbox-host mailbox gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P157 gate must run on the selected Mac, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != config.MacAccount {
		t.Fatalf("P157 account=%v err=%v, want %s", current, err, config.MacAccount)
	}

	loaded, err := config.LoadFile(p155MacConfigPath)
	if err != nil {
		t.Fatalf("load installed config: %v", err)
	}
	if loaded.SchemaVersion() != config.VersionV2 {
		t.Fatalf("installed schema version=%d, want %d", loaded.SchemaVersion(), config.VersionV2)
	}
	defaultMailbox, ok := loaded.Mailbox("default")
	if !ok {
		t.Fatal("installed V2 config has no default mailbox")
	}
	analyticsMailbox, ok := loaded.Mailbox(p157MailboxID)
	if !ok {
		t.Fatalf("installed V2 config has no %s mailbox", p157MailboxID)
	}
	if defaultMailbox.DefaultExecution != "mac-local" || analyticsMailbox.DefaultExecution != "mac-local" ||
		p155Contains(defaultMailbox.AllowedExecution, p157ContextName) ||
		!p155Contains(analyticsMailbox.AllowedExecution, p157ContextName) ||
		!p155Contains(analyticsMailbox.AllowedExecution, "ubuntu-current") {
		t.Fatalf("installed mailbox allow-lists do not preserve P155/default P157 policy: default=%+v analytics=%+v", defaultMailbox, analyticsMailbox)
	}
	context, ok := loaded.ExecutionContext(p157ContextName)
	if !ok || context.Environment != p157Environment || context.Target.Kind() != domain.TargetKindRemote || context.Target.Profile() != p157RemoteProfile {
		t.Fatalf("sandbox execution context=%+v present=%t", context, ok)
	}
	sandboxHost, ok := loaded.RemoteHost(p157RemoteProfile)
	if !ok || sandboxHost.Account != config.LinuxAccount || sandboxHost.QueuedBridge == nil || sandboxHost.DirectEndpoint == nil ||
		sandboxHost.QueuedBridge.Host != p157BridgeHost || sandboxHost.QueuedBridge.Port != p157BridgePort ||
		sandboxHost.DirectEndpoint.Name != p157DirectEndpoint || sandboxHost.DirectEndpoint.Endpoint != p157DirectURL {
		t.Fatalf("sandbox remote profile is not the P157 route: %+v present=%t", sandboxHost, ok)
	}
	currentHost, ok := loaded.RemoteHost("linux-host")
	if !ok || currentHost.QueuedBridge == nil || currentHost.DirectEndpoint == nil ||
		sandboxHost.QueuedBridge.KnownHostsFile == currentHost.QueuedBridge.KnownHostsFile ||
		sandboxHost.QueuedBridge.PrivateKeyFile == currentHost.QueuedBridge.PrivateKeyFile ||
		sandboxHost.DirectEndpoint.ClientCertificate == currentHost.DirectEndpoint.ClientCertificate ||
		sandboxHost.DirectEndpoint.ClientPrivateKeyFile == currentHost.DirectEndpoint.ClientPrivateKeyFile {
		t.Fatalf("sandbox profile does not use independent bridge/direct client paths: sandbox=%+v current=%+v", sandboxHost, currentHost)
	}

	p155WaitForLoadedLaunchAgent(t, "com.remote-session-runner.local")
	p155WaitForLoadedLaunchAgent(t, "com.remote-session-runner.locald")
	localSocket := filepath.Join(p155MacServiceRoot, "run", "local-api.sock")
	p155WaitForOwnedSocket(t, localSocket)
	p155RequireMailboxTree(t, analyticsMailbox.Root)
	p157WaitForSandboxRouter(t, localSocket)

	client, err := mailboxclient.New(analyticsMailbox.Root)
	if err != nil {
		t.Fatalf("open analytics mailbox client: %v", err)
	}
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	requestID := "req-p157-" + suffix
	expectedOutput := strings.Join([]string{p157ExpectedMarker, config.LinuxAccount, p157ExpectedHost, p157ExpectedArch, ""}, "\n")
	request := p155Request(t, map[string]any{
		"request_id": requestID, "idempotency_key": "key-p157-" + suffix,
		"operation": "run", "environment": p157Environment,
		"execution_target": map[string]string{"kind": "remote", "profile": p157RemoteProfile},
		"script":           "printf 'P157_SANDBOX_OK\\n'; id -un; hostname; uname -m",
	})
	if err := client.WriteRequest(requestID, request); err != nil {
		t.Fatalf("publish sandbox request: %v", err)
	}
	response := p155WaitTerminalResponse(t, client, requestID)
	p155AssertRunResponse(t, response, p157MailboxID, config.MailboxExecutionSelectionSourceRequestOverride,
		p157Environment, "remote", p157RemoteProfile, expectedOutput)
	if response.Stdout != expectedOutput {
		t.Fatalf("sandbox stdout=%q, want %q", response.Stdout, expectedOutput)
	}
	p157AssertExactEvents(t, client, response, expectedOutput)
	if err := client.WriteAcknowledgment(requestID, response); err != nil {
		t.Fatalf("publish sandbox ACK: %v", err)
	}
	p155WaitAckConsumed(t, analyticsMailbox.Root, requestID)
	p155WaitRequestConsumed(t, analyticsMailbox.Root, requestID)
	p157WaitForSandboxRouter(t, localSocket)
	t.Logf("P157 complete: request_id=%s command=%s", requestID, response.CommandID)
}

func p157WaitForSandboxRouter(t *testing.T, socketPath string) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var last any
	for {
		report, err := p155ReadHealth(socketPath)
		if err == nil && report.Readiness == "ready" && report.Metrics.MailboxBacklog == 0 &&
			p155HealthCheckIs(report, "remote_router/"+p157RemoteProfile, "ready") {
			return
		}
		if err != nil {
			last = err
		} else {
			last = report
		}
		if time.Now().After(deadline) {
			t.Fatalf("sandbox router did not become ready: %v", last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func p157AssertExactEvents(t *testing.T, client *mailboxclient.Client, response mailboxclient.Response, expectedOutput string) {
	t.Helper()
	events, err := client.ReadEventsThroughCursor(response)
	if err != nil {
		t.Fatalf("read sandbox events: %v", err)
	}
	var output strings.Builder
	for _, event := range events {
		if event.Type == "stdout" {
			if event.Encoding != "utf8" {
				t.Fatalf("sandbox stdout encoding=%q", event.Encoding)
			}
			output.WriteString(event.Text)
		}
	}
	if output.String() != expectedOutput {
		t.Fatalf("sandbox event stdout=%q, want %q", output.String(), expectedOutput)
	}
}
