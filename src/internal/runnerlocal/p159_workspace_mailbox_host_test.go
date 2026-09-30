package runnerlocal

import (
	"encoding/json"
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

const p159WorkspaceIngressMode os.FileMode = 0o644

// TestP159SlideStudioWorkspaceMailboxDefaultSandboxGate proves the direct
// workspace-file compatibility path. It deliberately does not use
// mailboxclient for publication: the request and ACK are ordinary exact-0644
// JSON-plus-marker pairs, while mailboxclient is used only to safely read the
// Runner-produced private response and event projection.
func TestP159SlideStudioWorkspaceMailboxDefaultSandboxGate(t *testing.T) {
	if os.Getenv("RSR_P159_SLIDESTUD_WORKSPACE_MAILBOX_GATE") != "1" {
		t.Skip("set RSR_P159_SLIDESTUD_WORKSPACE_MAILBOX_GATE=1 to run the direct SlideStudio workspace-mailbox gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P159 gate must run on the selected Mac, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != config.MacAccount {
		t.Fatalf("P159 account=%v err=%v, want %s", current, err, config.MacAccount)
	}

	loaded, err := config.LoadFile(p155MacConfigPath)
	if err != nil {
		t.Fatalf("load installed config: %v", err)
	}
	if loaded.SchemaVersion() != config.VersionV2 {
		t.Fatalf("installed schema version=%d, want %d", loaded.SchemaVersion(), config.VersionV2)
	}
	mailbox, ok := loaded.Mailbox(p158MailboxID)
	if !ok || mailbox.Root != p158MailboxRoot || mailbox.DefaultExecution != p157ContextName ||
		!p155Contains(mailbox.RepositoryAliases, p158MailboxID) ||
		!p155Contains(mailbox.AllowedExecution, p157ContextName) {
		t.Fatalf("installed P159 mailbox policy=%+v present=%t", mailbox, ok)
	}
	context, ok := loaded.ExecutionContext(p157ContextName)
	if !ok || context.Environment != p157Environment || context.Target.Kind() != domain.TargetKindRemote || context.Target.Profile() != p157RemoteProfile {
		t.Fatalf("sandbox default context=%+v present=%t", context, ok)
	}

	p155WaitForLoadedLaunchAgent(t, "com.remote-session-runner.local")
	p155WaitForLoadedLaunchAgent(t, "com.remote-session-runner.locald")
	localSocket := filepath.Join(p155MacServiceRoot, "run", "local-api.sock")
	p155WaitForOwnedSocket(t, localSocket)
	p158RequireSafeExternalAncestorChain(t, p158MailboxRoot)
	p155RequireMailboxTree(t, mailbox.Root)
	p157WaitForSandboxRouter(t, localSocket)

	client, err := mailboxclient.New(mailbox.Root)
	if err != nil {
		t.Fatalf("open SlideStudio mailbox reader: %v", err)
	}
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	requestID := "req-p159-" + suffix
	request := p155Request(t, map[string]any{
		"request_id": requestID, "idempotency_key": "key-p159-" + suffix,
		"operation": "run", "repository_alias": p158MailboxID,
		"script": "uname -a",
		// environment and execution_target are deliberately omitted so this
		// request uses the configured slidestud-io sandbox default.
	})
	p159WriteWorkspacePair(t, filepath.Join(mailbox.Root, "inbox"), requestID, request)

	response := p155WaitTerminalResponse(t, client, requestID)
	p155AssertRunResponse(t, response, p158MailboxID, config.MailboxExecutionSelectionSourceInboxDefault,
		p157Environment, "remote", p157RemoteProfile, "Linux ")
	if !strings.Contains(response.Stdout, p157ExpectedHost) || !strings.Contains(response.Stdout, p157ExpectedArch) {
		t.Fatalf("P159 uname output=%q, want sandbox host %q and architecture %q", response.Stdout, p157ExpectedHost, p157ExpectedArch)
	}
	p159RequirePrivateProjection(t, filepath.Join(mailbox.Root, "outbox", requestID+".json"))
	p159RequirePrivateProjection(t, filepath.Join(mailbox.Root, filepath.FromSlash(response.EventsFile)))
	p157AssertExactEvents(t, client, response, response.Stdout)

	ack, err := json.Marshal(mailboxclient.Acknowledgment{
		RequestID: requestID, ResponseRevision: response.ResponseRevision,
		AvailableEventSequence: response.AvailableEventSequence,
	})
	if err != nil {
		t.Fatal(err)
	}
	p159WriteWorkspacePair(t, filepath.Join(mailbox.Root, "acks"), requestID, ack)
	p155WaitAckConsumed(t, mailbox.Root, requestID)
	p155WaitRequestConsumed(t, mailbox.Root, requestID)
	p157WaitForSandboxRouter(t, localSocket)
	t.Logf("P159 complete: direct-0644 request_id=%s command=%s", requestID, response.CommandID)
}

func p159WriteWorkspacePair(t *testing.T, directory, requestID string, jsonBytes []byte) {
	t.Helper()
	p159WriteWorkspaceFile(t, filepath.Join(directory, requestID+".json"), jsonBytes)
	p159WriteWorkspaceFile(t, filepath.Join(directory, requestID+".ready"), nil)
}

func p159WriteWorkspaceFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, p159WorkspaceIngressMode)
	if err != nil {
		t.Fatalf("create direct workspace mailbox file %s: %v", path, err)
	}
	if len(contents) > 0 {
		if _, err := file.Write(contents); err != nil {
			_ = file.Close()
			t.Fatalf("write direct workspace mailbox file %s: %v", path, err)
		}
	}
	if err := file.Chmod(p159WorkspaceIngressMode); err != nil {
		_ = file.Close()
		t.Fatalf("chmod direct workspace mailbox file %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close direct workspace mailbox file %s: %v", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != p159WorkspaceIngressMode {
		t.Fatalf("direct workspace mailbox file %s info=%v err=%v", path, info, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		t.Fatalf("direct workspace mailbox file %s owner=%v, want uid %d", path, info.Sys(), os.Geteuid())
	}
}

func p159RequirePrivateProjection(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("Runner projection %s info=%v err=%v, want regular 0600", path, info, err)
	}
}
