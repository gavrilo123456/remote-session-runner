//go:build bug018host

package runnerlocal

import (
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/mailboxclient"
)

// TestBUG018SlideStudioExplicitMacLocalRun proves the installed ingress accepts
// the documented wire tuple for the configured mac-local context. It is opt-in
// because it publishes one harmless local command and consumes its own ACK.
func TestBUG018SlideStudioExplicitMacLocalRun(t *testing.T) {
	if os.Getenv("RSR_BUG018_SLIDESTUD_MAC_LOCAL_GATE") != "1" {
		t.Skip("set RSR_BUG018_SLIDESTUD_MAC_LOCAL_GATE=1 to run the installed BUG-018 mailbox gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("BUG-018 installed mailbox gate must run on the selected Mac, got %s", runtime.GOOS)
	}
	loaded, err := config.LoadFile(p155MacConfigPath)
	if err != nil {
		t.Fatalf("load installed config: %v", err)
	}
	mailbox, ok := loaded.Mailbox(p158MailboxID)
	if !ok || mailbox.Root != p158MailboxRoot || !p155Contains(mailbox.AllowedExecution, "mac-local") {
		t.Fatalf("installed SlideStudio mailbox policy=%+v present=%t", mailbox, ok)
	}
	context, ok := loaded.ExecutionContext("mac-local")
	if !ok || context.Environment != "mac-dev" || context.Target.Kind() != domain.TargetKindLocal || context.Target.Profile() != "mac-workstation" {
		t.Fatalf("installed mac-local context=%+v present=%t", context, ok)
	}
	client, err := mailboxclient.New(mailbox.Root)
	if err != nil {
		t.Fatalf("open SlideStudio mailbox client: %v", err)
	}
	suffix := fmt.Sprintf("%x", time.Now().UTC().UnixNano())
	requestID := "req-bug018-slidestud-mac-local-" + suffix
	request := p155Request(t, map[string]any{
		"request_id": requestID, "idempotency_key": "key-bug018-" + suffix,
		"operation": "run", "repository_alias": p158MailboxID,
		"environment": "mac-dev",
		"execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
		"script":           "printf 'BUG018_SLIDESTUD_MAC_LOCAL_OK\\n'; id -un",
	})
	if err := client.WriteRequest(requestID, request); err != nil {
		t.Fatalf("publish BUG-018 request: %v", err)
	}
	response := p155WaitTerminalResponse(t, client, requestID)
	p155AssertRunResponse(t, response, p158MailboxID, config.MailboxExecutionSelectionSourceRequestOverride,
		"mac-dev", "local", "mac-workstation", "BUG018_SLIDESTUD_MAC_LOCAL_OK\\ntomasz.walczuk\\n")
	if err := client.WriteAcknowledgment(requestID, response); err != nil {
		t.Fatalf("publish BUG-018 ACK: %v", err)
	}
	p155WaitAckConsumed(t, mailbox.Root, requestID)
	p155WaitRequestConsumed(t, mailbox.Root, requestID)
	t.Logf("BUG-018 complete: request_id=%s command_id=%s", requestID, response.CommandID)
}
