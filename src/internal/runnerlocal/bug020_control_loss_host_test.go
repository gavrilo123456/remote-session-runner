//go:build bug020host

package runnerlocal

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
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

// These installed gates are deliberately separate. The loss gate leaves its
// own durable lost pair for separately approved maintenance; it never stops,
// recovers, restarts, or changes an installed service. Run the normal gate only
// after maintenance has proven zero capacity. Both require the exact installed
// commit and a pre-created owner-only evidence directory under /private/tmp.
//
// Mac, from the source checkout, using its shared Go caches:
// RSR_BUG020_EXPECTED_COMMIT=<40-character-sha> RSR_BUG020_EVIDENCE_DIR=<directory>
// RSR_BUG020_MAC_LOCAL_LOSS_GATE=1 go test -tags bug020host ./src/internal/runnerlocal -run '^TestBUG020InstalledMacLocalChildLossMailboxGate$' -count=1 -v
// RSR_BUG020_MAC_LOCAL_POSTFLIGHT_GATE=1 go test -tags bug020host ./src/internal/runnerlocal -run '^TestBUG020InstalledMacLocalNormalMailboxPostflight$' -count=1 -v
// Each invocation must include the same expected-commit/evidence variables.

type bug020InstalledHealth struct {
	Component     string `json:"component"`
	BuildRevision string `json:"build_revision"`
	Readiness     string `json:"readiness"`
	Metrics       *struct {
		ActiveSessionSlots *int64 `json:"active_session_slots"`
		ActiveCommandSlots *int64 `json:"active_command_slots"`
		QueuedCommands     *int64 `json:"queued_commands"`
	} `json:"metrics"`
}

type bug020InstalledGate struct {
	client      *mailboxclient.Client
	commit      string
	evidenceDir string
	locald      string
}

func TestBUG020InstalledMacLocalChildLossMailboxGate(t *testing.T) {
	gate := bug020InstalledPreflight(t, "RSR_BUG020_MAC_LOCAL_LOSS_GATE")
	// The short prefix pause lets the harmless marker become a durable output
	// event. The background child keeps only the inherited control writer open
	// for eight seconds after Bash exits, longer than the five-second gate.
	const marker = "BUG020_CONTROL_LOSS_BODY_ONCE\n"
	script := "printf 'BUG020_CONTROL_LOSS_BODY_ONCE\\n'\n" +
		"( exec /bin/sleep 8 ) </dev/null >/dev/null 2>&1 &\n/bin/sleep 0.3\nexit 0\n"
	requestID, response := gate.run(t, "loss", script, 5*time.Second)
	if response.RequestState != "complete" || response.JobPhase != "lost" || response.CommandState != "lost" ||
		response.ExitCode != nil || response.OutputComplete == nil || *response.OutputComplete ||
		response.OutputUnavailableReason != "capture_boundary_unconfirmed" || response.TeardownOutcome != "lost" {
		t.Fatalf("BUG020 loss result: request_state=%s job_phase=%s command_state=%s output_complete=%v reason=%q teardown=%s",
			response.RequestState, response.JobPhase, response.CommandState, response.OutputComplete, response.OutputUnavailableReason, response.TeardownOutcome)
	}
	events := bug020InstalledEvents(t, gate.client, response, "command_lost", marker)
	// Observe several installed reconciliation passes without resubmitting the
	// script or reusing its idempotency key. The frozen result must stay exact.
	for range 10 {
		time.Sleep(100 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		current, err := gate.client.WaitResponse(ctx, requestID)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		before, _ := json.Marshal(response)
		after, _ := json.Marshal(current)
		if string(before) != string(after) {
			t.Fatal("BUG020 terminal response changed during reconciliation")
		}
		bug020InstalledEvents(t, gate.client, current, "command_lost", marker)
	}
	gate.preserveAndACK(t, response, events)
	t.Logf("BUG020 loss gate ACKed only its proved response: request_id=%s session_id=%s command_id=%s job_id=%s commit=%s; own lost pair requires separately approved maintenance",
		requestID, response.SessionID, response.CommandID, response.JobID, gate.commit)
}

func TestBUG020InstalledMacLocalNormalMailboxPostflight(t *testing.T) {
	gate := bug020InstalledPreflight(t, "RSR_BUG020_MAC_LOCAL_POSTFLIGHT_GATE")
	const marker = "BUG020_NORMAL_POSTFLIGHT_OK\n"
	_, response := gate.run(t, "postflight", "printf 'BUG020_NORMAL_POSTFLIGHT_OK\\n'\n", 30*time.Second)
	p155AssertRunResponse(t, response, p158MailboxID, config.MailboxExecutionSelectionSourceRequestOverride,
		"mac-dev", "local", "mac-workstation", marker)
	events := bug020InstalledEvents(t, gate.client, response, "command_succeeded", marker)
	gate.preserveAndACK(t, response, events)
	deadline := time.Now().Add(15 * time.Second)
	for {
		report, err := bug020ReadInstalledHealth(gate.locald)
		if err == nil && bug020InstalledCapacityZero(report, gate.commit) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("BUG020 normal teardown did not return installed local capacity to zero: revision=%s readiness=%s err=%v", report.BuildRevision, report.Readiness, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("BUG020 postflight complete: request_id=%s session_id=%s command_id=%s commit=%s ACK consumed and local session/command/queued capacity zero",
		response.RequestID, response.SessionID, response.CommandID, gate.commit)
}

func bug020InstalledPreflight(t *testing.T, gateVariable string) bug020InstalledGate {
	t.Helper()
	if os.Getenv(gateVariable) != "1" {
		t.Skipf("set %s=1 for the installed BUG020 mailbox gate", gateVariable)
	}
	if runtime.GOOS != "darwin" {
		t.Fatal("BUG020 installed gate requires the selected Mac")
	}
	current, err := user.Current()
	if err != nil || current.Username != config.MacAccount {
		t.Fatalf("BUG020 installed gate requires account %s", config.MacAccount)
	}
	commit := os.Getenv("RSR_BUG020_EXPECTED_COMMIT")
	if len(commit) != 40 || strings.Trim(commit, "0123456789abcdef") != "" {
		t.Fatal("RSR_BUG020_EXPECTED_COMMIT must be the exact 40-character lowercase serving commit")
	}
	evidenceDir := os.Getenv("RSR_BUG020_EVIDENCE_DIR")
	if !filepath.IsAbs(evidenceDir) || !strings.HasPrefix(filepath.Clean(evidenceDir), "/private/tmp/") {
		t.Fatal("RSR_BUG020_EVIDENCE_DIR must be a task-owned absolute directory under /private/tmp")
	}
	info, err := os.Lstat(evidenceDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		t.Fatal("BUG020 evidence directory must already exist as an owner-only 0700 directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		t.Fatal("BUG020 evidence directory must be owned by the selected Mac account")
	}
	loaded, err := config.LoadFile(p155MacConfigPath)
	if err != nil {
		t.Fatalf("load installed BUG020 configuration: %v", err)
	}
	mailbox, ok := loaded.Mailbox(p158MailboxID)
	if !ok || mailbox.Root != p158MailboxRoot || !p155Contains(mailbox.RepositoryAliases, p158MailboxID) || !p155Contains(mailbox.AllowedExecution, "mac-local") {
		t.Fatal("installed SlideStudio mailbox does not authorize the expected local route")
	}
	executionContext, ok := loaded.ExecutionContext("mac-local")
	if !ok || executionContext.Environment != "mac-dev" || executionContext.Target.Kind() != domain.TargetKindLocal || executionContext.Target.Profile() != "mac-workstation" {
		t.Fatal("installed mac-local context does not match mac-dev/local/mac-workstation")
	}
	p158RequireSafeExternalAncestorChain(t, mailbox.Root)
	p155RequireMailboxTree(t, mailbox.Root)
	locald := filepath.Join(p155MacServiceRoot, "run", "locald.sock")
	for _, endpoint := range []struct{ path, component string }{
		{filepath.Join(p155MacServiceRoot, "run", "local-api.sock"), "mac_ingress"}, {locald, "mac_local_executor"},
	} {
		if err := p155OwnedSocketError(endpoint.path); err != nil {
			t.Fatalf("BUG020 serving owner socket is unavailable: %v", err)
		}
		report, err := bug020ReadInstalledHealth(endpoint.path)
		if err != nil || report.Component != endpoint.component || report.Readiness != "ready" || report.BuildRevision != commit {
			t.Fatalf("BUG020 serving identity: component=%s revision=%s readiness=%s err=%v", report.Component, report.BuildRevision, report.Readiness, err)
		}
		if endpoint.path == locald && !bug020InstalledCapacityZero(report, commit) {
			t.Fatal("BUG020 installed locald preflight requires zero session, command, and queued capacity; no request published")
		}
	}
	client, err := mailboxclient.New(mailbox.Root)
	if err != nil {
		t.Fatal(err)
	}
	return bug020InstalledGate{client: client, commit: commit, evidenceDir: evidenceDir, locald: locald}
}

func (gate bug020InstalledGate) run(t *testing.T, label, script string, timeout time.Duration) (string, mailboxclient.Response) {
	t.Helper()
	suffix := fmt.Sprintf("%x", time.Now().UTC().UnixNano())
	requestID := "req-bug020-" + label + "-" + suffix
	request := p155Request(t, map[string]any{
		"request_id": requestID, "idempotency_key": "key-bug020-" + label + "-" + suffix,
		"operation": "run", "repository_alias": p158MailboxID, "environment": "mac-dev",
		"execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"}, "script": script,
	})
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := gate.client.WriteRequest(requestID, request); err != nil {
		t.Fatalf("publish BUG020 %s request: %v", label, err)
	}
	t.Logf("BUG020 published its own request_id=%s commit=%s", requestID, gate.commit)
	var identity mailboxclient.Response
	for {
		response, err := gate.client.WaitResponse(ctx, requestID)
		if err != nil {
			t.Fatalf("BUG020 %s request %s did not settle in %s: %v; left unACKed", label, requestID, timeout, err)
		}
		if identity.CommandID == "" && response.CommandID != "" {
			identity = response
		}
		if identity.CommandID != "" && (response.CommandID != identity.CommandID || response.SessionID != identity.SessionID || response.JobID != identity.JobID) {
			t.Fatal("BUG020 installed request changed command/session/job identity; left unACKed")
		}
		if response.RequestState == "complete" || response.RequestState == "rejected" || response.RequestState == "indeterminate" {
			if time.Since(started) >= timeout || response.CommandID == "" || response.SessionID == "" || response.JobID == "" ||
				response.InboxID != p158MailboxID || response.Operation != "run" || response.ResolvedEnvironment != "mac-dev" ||
				response.ExecutionSelectionSource != config.MailboxExecutionSelectionSourceRequestOverride ||
				response.ResolvedExecutionTarget == nil || response.ResolvedExecutionTarget.Kind != "local" || response.ResolvedExecutionTarget.Profile != "mac-workstation" {
				t.Fatal("BUG020 terminal response lacks bounded expected route/identity; left unACKed")
			}
			return requestID, response
		}
		select {
		case <-ctx.Done():
			t.Fatalf("BUG020 %s request %s remained nonterminal beyond %s; left unACKed", label, requestID, timeout)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func bug020InstalledEvents(t *testing.T, client *mailboxclient.Client, response mailboxclient.Response, terminalType, marker string) []mailboxclient.Event {
	t.Helper()
	if response.FinalEventSequence == nil || response.AvailableEventSequence == nil || *response.FinalEventSequence < 1 || *response.FinalEventSequence != *response.AvailableEventSequence {
		t.Fatal("BUG020 terminal response lacks a complete advertised final event cursor; left unACKed")
	}
	events, err := client.ReadEventsThroughCursor(response)
	if err != nil || int64(len(events)) != *response.FinalEventSequence {
		t.Fatalf("BUG020 advertised event prefix is unavailable or incomplete: %v; left unACKed", err)
	}
	started, terminal := 0, 0
	var stdout strings.Builder
	for _, event := range events {
		if event.Type == "command_started" {
			started++
		}
		switch event.Type {
		case "command_succeeded", "command_failed", "command_cancelled", "command_timed_out", "command_rejected", "command_lost":
			terminal++
		}
		if event.Type == "stdout" && event.Encoding == "utf8" {
			stdout.WriteString(event.Text)
		}
	}
	if started != 1 || terminal != 1 || events[len(events)-1].Type != terminalType || strings.Count(stdout.String(), marker) != 1 {
		t.Fatalf("BUG020 lifecycle prefix: starts=%d terminals=%d final_type=%s marker_count=%d; left unACKed", started, terminal, events[len(events)-1].Type, strings.Count(stdout.String(), marker))
	}
	return events
}

func (gate bug020InstalledGate) preserveAndACK(t *testing.T, response mailboxclient.Response, events []mailboxclient.Event) {
	t.Helper()
	evidence, err := json.MarshalIndent(struct {
		Commit   string                 `json:"serving_commit"`
		Response mailboxclient.Response `json:"acknowledged_response"`
		Events   []mailboxclient.Event  `json:"advertised_events"`
	}{gate.commit, response, events}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(gate.evidenceDir, response.RequestID+".json")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("preserve BUG020 evidence before ACK: %v", err)
	}
	if _, err = file.Write(append(evidence, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("sync BUG020 evidence before ACK: write=%v close=%v", err, closeErr)
	}
	t.Logf("BUG020 preserved proved response/events before exact ACK: %s", path)
	if err := gate.client.WriteAcknowledgment(response.RequestID, response); err != nil {
		t.Fatalf("ACK only BUG020 proved response: %v", err)
	}
	p155WaitAckConsumed(t, p158MailboxRoot, response.RequestID)
	p155WaitRequestConsumed(t, p158MailboxRoot, response.RequestID)
}

func bug020InstalledCapacityZero(report bug020InstalledHealth, commit string) bool {
	return report.Component == "mac_local_executor" && report.BuildRevision == commit && report.Readiness == "ready" && report.Metrics != nil &&
		report.Metrics.ActiveSessionSlots != nil && *report.Metrics.ActiveSessionSlots == 0 &&
		report.Metrics.ActiveCommandSlots != nil && *report.Metrics.ActiveCommandSlots == 0 &&
		report.Metrics.QueuedCommands != nil && *report.Metrics.QueuedCommands == 0
}

func bug020ReadInstalledHealth(socketPath string) (bug020InstalledHealth, error) {
	dialer := &net.Dialer{}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socketPath)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	response, err := client.Get("http://runner/health/ready")
	if err != nil {
		return bug020InstalledHealth{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return bug020InstalledHealth{}, fmt.Errorf("health status=%s", response.Status)
	}
	var report bug020InstalledHealth
	if err := json.NewDecoder(response.Body).Decode(&report); err != nil {
		return bug020InstalledHealth{}, err
	}
	return report, nil
}
