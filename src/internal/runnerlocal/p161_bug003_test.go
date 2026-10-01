package runnerlocal

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"remote-session-runner/src/internal/dispatcher"
	"remote-session-runner/src/internal/mailbox"
)

const p161SecretLogText = "BUG003_SECRET_LOG_TEXT"

// TestBUG003ReconciliationLogsContainOnlySafeDiagnosticFields proves that the
// service emits stable identifiers and route labels while leaving arbitrary
// underlying errors out of its operational log.
func TestBUG003ReconciliationLogsContainOnlySafeDiagnosticFields(t *testing.T) {
	service := &Service{remoteEndpoints: map[string]string{"sandbox-host": "132.226.205.205:8443"}}

	var remote bytes.Buffer
	service.logRemoteReconciliationError(&remote, &dispatcher.RemoteReconciliationIssue{
		Operation: "run", IntentID: "intent-bug003", JobID: "job-bug003", SessionID: "sess-bug003",
		CommandID: "cmd-bug003", TargetProfile: "sandbox-host", FailureClass: "remote_status_unavailable", RetryCount: 2,
	})
	line := remote.String()
	for _, want := range []string{
		"remote_reconciliation", "operation=run", "intent_id=intent-bug003", "job_id=job-bug003",
		"session_id=sess-bug003", "command_id=cmd-bug003", "target_profile=sandbox-host",
		"endpoint=132.226.205.205:8443", "failure_class=remote_status_unavailable", "retry_count=2",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("remote diagnostic %q is missing %q", line, want)
		}
	}

	var mailboxLog bytes.Buffer
	service.logMailboxReconciliationError(&mailboxLog, "slidestud-io", "reconcile", &mailbox.ReconciliationIssue{
		MailboxID: "slidestud-io", Stage: "accepted_run", Operation: "run", RequestID: "req-bug003",
		JobID: "job-bug003", SessionID: "sess-bug003", CommandID: "cmd-bug003", TargetProfile: "sandbox-host",
		FailureClass: "stored_response_invalid",
	})
	line = mailboxLog.String()
	for _, want := range []string{
		"mailbox_reconciliation", "mailbox=slidestud-io", "stage=accepted_run", "operation=run",
		"request_id=req-bug003", "endpoint=132.226.205.205:8443", "failure_class=stored_response_invalid",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("mailbox diagnostic %q is missing %q", line, want)
		}
	}

	var fallback bytes.Buffer
	service.logRemoteReconciliationError(&fallback, errors.New(p161SecretLogText))
	service.logMailboxReconciliationError(&fallback, "slidestud-io", "reconcile", errors.New(p161SecretLogText))
	if strings.Contains(fallback.String(), p161SecretLogText) {
		t.Fatalf("safe fallback diagnostics leaked raw error: %q", fallback.String())
	}
}
