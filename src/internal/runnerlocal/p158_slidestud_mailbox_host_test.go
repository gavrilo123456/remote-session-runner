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
	p158MailboxID       = "slidestud-io"
	p158MailboxRoot     = "/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-"
	p158ExpectedMarker  = "SLIDESTUD_MAILBOX_DEFAULT_SANDBOX_OK"
	p158ExpectedSandbox = "SLIDESTUD_MAILBOX_DEFAULT_SANDBOX_OK\nubuntu\noracle-gustaw-janecki-ubuntu-flex-02\naarch64\n"
)

// TestP158SlideStudioExternalMailboxDefaultSandboxGate proves the installed
// external mailbox with a native marker-last exchange. It is opt-in because
// it submits one harmless command. The request deliberately omits both target
// fields, so the terminal response must prove the configured inbox default.
func TestP158SlideStudioExternalMailboxDefaultSandboxGate(t *testing.T) {
	if os.Getenv("RSR_P158_SLIDESTUD_MAILBOX_GATE") != "1" {
		t.Skip("set RSR_P158_SLIDESTUD_MAILBOX_GATE=1 to run the SlideStudio external-mailbox gate")
	}
	p158RunDefaultSandboxMailbox(t, true, "req-p158-", "P158")
}

// TestP158SlideStudioExternalMailboxDefaultSandboxWithExistingBacklog proves
// the native external inbox can complete one new default-sandbox request while
// unrelated work is retained. It is intentionally separate from P158: it
// proves the request's own terminal lifecycle, not mailbox quiescence or P128.
func TestP158SlideStudioExternalMailboxDefaultSandboxWithExistingBacklog(t *testing.T) {
	if os.Getenv("RSR_P158_SLIDESTUD_MAILBOX_BACKLOG_GATE") != "1" {
		t.Skip("set RSR_P158_SLIDESTUD_MAILBOX_BACKLOG_GATE=1 to run the nonquiescent SlideStudio route-only gate")
	}
	p158RunDefaultSandboxMailbox(t, false, "req-p158-backlog-", "P158 route-only")
}

func p158RunDefaultSandboxMailbox(t *testing.T, requireQuiescence bool, requestPrefix, label string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Fatalf("%s gate must run on the selected Mac, got %s", label, runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != config.MacAccount {
		t.Fatalf("%s account=%v err=%v, want %s", label, current, err, config.MacAccount)
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
		!p155Contains(mailbox.AllowedExecution, p157ContextName) ||
		!p155Contains(mailbox.AllowedExecution, "mac-local") || !p155Contains(mailbox.AllowedExecution, "ubuntu-current") {
		t.Fatalf("installed P158 mailbox policy=%+v present=%t", mailbox, ok)
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
	if requireQuiescence {
		p157WaitForSandboxRouter(t, localSocket)
	} else {
		before := p157WaitForSandboxRouterReady(t, localSocket)
		if !p158HasRetainedSlideStudioBacklog(before) {
			t.Fatalf("%s requires pre-existing retained SlideStudio backlog: health=%+v", label, before)
		}
		t.Logf("%s preflight: mailbox_backlog=%d mailbox_backlog_by_inbox=%v; route-only, not a drain or P128 claim", label, before.Metrics.MailboxBacklog, before.Metrics.MailboxBacklogByInbox)
	}

	client, err := mailboxclient.New(mailbox.Root)
	if err != nil {
		t.Fatalf("open SlideStudio mailbox client: %v", err)
	}
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	requestID := requestPrefix + suffix
	request := p155Request(t, map[string]any{
		"request_id": requestID, "idempotency_key": "key-p158-" + suffix,
		"operation": "run", "repository_alias": p158MailboxID,
		"script": "printf 'SLIDESTUD_MAILBOX_DEFAULT_SANDBOX_OK\\n'; id -un; hostname; uname -m",
		// environment and execution_target are deliberately omitted.
	})
	if err := client.WriteRequest(requestID, request); err != nil {
		t.Fatalf("publish SlideStudio request: %v", err)
	}
	response := p155WaitTerminalResponse(t, client, requestID)
	p155AssertRunResponse(t, response, p158MailboxID, config.MailboxExecutionSelectionSourceInboxDefault,
		p157Environment, "remote", p157RemoteProfile, p158ExpectedSandbox)
	if response.Stdout != p158ExpectedSandbox {
		t.Fatalf("SlideStudio stdout=%q, want %q", response.Stdout, p158ExpectedSandbox)
	}
	p157AssertExactEvents(t, client, response, p158ExpectedSandbox)
	if err := client.WriteAcknowledgment(requestID, response); err != nil {
		t.Fatalf("publish SlideStudio ACK: %v", err)
	}
	p155WaitAckConsumed(t, mailbox.Root, requestID)
	p155WaitRequestConsumed(t, mailbox.Root, requestID)
	if requireQuiescence {
		p157WaitForSandboxRouter(t, localSocket)
	} else {
		after := p157WaitForSandboxRouterReady(t, localSocket)
		t.Logf("%s postflight: mailbox_backlog=%d mailbox_backlog_by_inbox=%v; own request is terminal and consumed, and the test did not act on unrelated receipts", label, after.Metrics.MailboxBacklog, after.Metrics.MailboxBacklogByInbox)
	}
	t.Logf("%s complete: request_id=%s command=%s marker=%s", label, requestID, response.CommandID, p158ExpectedMarker)
}

func p158HasRetainedSlideStudioBacklog(report p155HealthReport) bool {
	return report.Metrics.MailboxBacklog > 0 && report.Metrics.MailboxBacklogByInbox[p158MailboxID] > 0
}

func TestP158HasRetainedSlideStudioBacklog(t *testing.T) {
	tests := []struct {
		name    string
		backlog int64
		byInbox map[string]int64
		want    bool
	}{
		{name: "same inbox retained work", backlog: 2, byInbox: map[string]int64{p158MailboxID: 2}, want: true},
		{name: "only another inbox has work", backlog: 2, byInbox: map[string]int64{"analytics": 2}, want: false},
		{name: "zero aggregate", backlog: 0, byInbox: map[string]int64{p158MailboxID: 1}, want: false},
		{name: "no per-inbox map", backlog: 2, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := p155HealthReport{}
			report.Metrics.MailboxBacklog = test.backlog
			report.Metrics.MailboxBacklogByInbox = test.byInbox
			if got := p158HasRetainedSlideStudioBacklog(report); got != test.want {
				t.Fatalf("p158HasRetainedSlideStudioBacklog()=%t, want %t", got, test.want)
			}
		})
	}
}

func p158RequireSafeExternalAncestorChain(t *testing.T, root string) {
	t.Helper()
	parent := filepath.Dir(root)
	trustedRoot := string(filepath.Separator)
	relative, err := filepath.Rel(trustedRoot, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("external mailbox parent is not below filesystem root: %q", parent)
	}
	current := trustedRoot
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
			t.Fatalf("unsafe external mailbox ancestor %s: info=%v err=%v", current, info, err)
		}
		if current == parent {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || int(stat.Uid) != os.Geteuid() {
				t.Fatalf("external mailbox parent %s is not owned by uid %d", parent, os.Geteuid())
			}
		}
	}
}
