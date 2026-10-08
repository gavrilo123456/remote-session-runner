package runnerlocal

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/mailboxclient"
)

const (
	p155MacServiceRoot = "/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner"
	p155MacConfigPath  = p155MacServiceRoot + "/config/mac.yaml"
)

type p155HealthCheck struct {
	Component string `json:"component"`
	State     string `json:"state"`
}

type p155HealthReport struct {
	Readiness string            `json:"readiness"`
	Checks    []p155HealthCheck `json:"checks"`
	Metrics   struct {
		MailboxBacklog        int64            `json:"mailbox_backlog"`
		MailboxBacklogByInbox map[string]int64 `json:"mailbox_backlog_by_inbox"`
	} `json:"metrics"`
}

// TestP155MacInstalledMultiInboxMailboxGate validates the real installed V2
// Mac services. It is opt-in because it writes two harmless marker-last
// requests to the selected owner-only mailbox roots. The test does not deploy,
// restart, reconfigure, or clean up either service.
func TestP155MacInstalledMultiInboxMailboxGate(t *testing.T) {
	if os.Getenv("RSR_P155_MAC_HOST_GATE") != "1" {
		t.Skip("set RSR_P155_MAC_HOST_GATE=1 to run the installed Mac multi-inbox gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P155 gate must run on the selected Mac, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != "tomasz.walczuk" {
		t.Fatalf("P155 account=%v err=%v, want tomasz.walczuk", current, err)
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
	analyticsMailbox, ok := loaded.Mailbox("analytics")
	if !ok {
		t.Fatal("installed V2 config has no analytics mailbox")
	}
	if defaultMailbox.Root != filepath.Join(p155MacServiceRoot, "mailbox") ||
		analyticsMailbox.Root != filepath.Join(p155MacServiceRoot, "mailboxes", "analytics") ||
		defaultMailbox.DefaultExecution != "mac-local" || analyticsMailbox.DefaultExecution != "mac-local" ||
		!p155Contains(defaultMailbox.AllowedExecution, "mac-local") ||
		!p155Contains(defaultMailbox.AllowedExecution, "ubuntu-current") ||
		!p155Contains(analyticsMailbox.AllowedExecution, "mac-local") ||
		!p155Contains(analyticsMailbox.AllowedExecution, "ubuntu-current") {
		t.Fatalf("installed mailbox policy is not the P155 candidate: default=%+v analytics=%+v", defaultMailbox, analyticsMailbox)
	}

	p155WaitForLoadedLaunchAgent(t, "com.remote-session-runner.local")
	p155WaitForLoadedLaunchAgent(t, "com.remote-session-runner.locald")
	localSocket := filepath.Join(p155MacServiceRoot, "run", "local-api.sock")
	localdSocket := filepath.Join(p155MacServiceRoot, "run", "locald.sock")
	p155WaitForOwnedSocket(t, localSocket)
	p155WaitForOwnedSocket(t, localdSocket)
	p155RequireInstalledConfigValidation(t)
	localHealth := p155WaitForReadyHealth(t, localSocket)
	if localHealth.Readiness != "ready" || localHealth.Metrics.MailboxBacklog != 0 || !p155CurrentHostRouterReady(localHealth) {
		t.Fatalf("initial Mac ingress health=%+v", localHealth)
	}
	if localdHealth := p155WaitForReadyHealth(t, localdSocket); localdHealth.Readiness != "ready" {
		t.Fatalf("initial Mac local executor health=%+v", localdHealth)
	}

	defaultClient, err := mailboxclient.New(defaultMailbox.Root)
	if err != nil {
		t.Fatalf("open default mailbox client: %v", err)
	}
	analyticsClient, err := mailboxclient.New(analyticsMailbox.Root)
	if err != nil {
		t.Fatalf("open analytics mailbox client: %v", err)
	}
	p155RequireMailboxTree(t, defaultMailbox.Root)
	p155RequireMailboxTree(t, analyticsMailbox.Root)

	// The same client-visible request and idempotency identities are used in
	// both roots. They must remain independent because every mailbox exchange
	// is scoped by its configured inbox ID.
	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	requestID := "req-p155-" + suffix
	idempotencyKey := "key-p155-" + suffix
	localRequest := p155Request(t, map[string]any{
		"request_id": requestID, "idempotency_key": idempotencyKey,
		"operation": "run", "repository_alias": "remote-session-runner",
		"script": "printf 'P155_LOCAL_OK\\n'; id -un",
	})
	remoteRequest := p155Request(t, map[string]any{
		"request_id": requestID, "idempotency_key": idempotencyKey,
		"operation": "run", "repository_alias": "analytics-dbt",
		"environment":      "linux-dev",
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
		"script":           "printf 'P155_REMOTE_OK\\n'; id -un; hostname",
	})
	if err := defaultClient.WriteRequest(requestID, localRequest); err != nil {
		t.Fatalf("publish default request: %v", err)
	}
	if err := analyticsClient.WriteRequest(requestID, remoteRequest); err != nil {
		t.Fatalf("publish analytics request: %v", err)
	}

	defaultResponse := p155WaitTerminalResponse(t, defaultClient, requestID)
	analyticsResponse := p155WaitTerminalResponse(t, analyticsClient, requestID)
	p155AssertRunResponse(t, defaultResponse, "default", "inbox_default", "mac-dev", "local", "mac-workstation", "P155_LOCAL_OK\ntomasz.walczuk\n")
	p155AssertRunResponse(t, analyticsResponse, "analytics", "request_override", "linux-dev", "remote", "linux-host", "P155_REMOTE_OK\nubuntu\n")
	p155AssertRemoteHostname(t, analyticsResponse.Stdout)
	if defaultResponse.CommandID == analyticsResponse.CommandID || defaultResponse.SessionID == analyticsResponse.SessionID || defaultResponse.JobID == analyticsResponse.JobID {
		t.Fatalf("same client IDs crossed inbox namespaces: default=%+v analytics=%+v", defaultResponse, analyticsResponse)
	}

	defaultEventPath := p155AssertEvents(t, defaultClient, defaultMailbox.Root, defaultResponse, "P155_LOCAL_OK\ntomasz.walczuk\n")
	analyticsEventPath := p155AssertEvents(t, analyticsClient, analyticsMailbox.Root, analyticsResponse, "P155_REMOTE_OK\nubuntu\n")
	if _, err := os.Stat(filepath.Join(analyticsMailbox.Root, defaultResponse.EventsFile)); !os.IsNotExist(err) {
		t.Fatalf("default event was projected into analytics root: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(defaultMailbox.Root, analyticsResponse.EventsFile)); !os.IsNotExist(err) {
		t.Fatalf("analytics event was projected into default root: err=%v", err)
	}
	if err := defaultClient.WriteAcknowledgment(requestID, defaultResponse); err != nil {
		t.Fatalf("publish default ACK: %v", err)
	}
	p155WaitAckConsumed(t, defaultMailbox.Root, requestID)
	for _, path := range []string{
		filepath.Join(analyticsMailbox.Root, "outbox", requestID+".json"),
		analyticsEventPath,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("default ACK altered analytics artifact %s: %v", path, err)
		}
	}
	if err := analyticsClient.WriteAcknowledgment(requestID, analyticsResponse); err != nil {
		t.Fatalf("publish analytics ACK: %v", err)
	}
	p155WaitAckConsumed(t, analyticsMailbox.Root, requestID)
	for _, path := range []string{
		filepath.Join(defaultMailbox.Root, "outbox", requestID+".json"),
		defaultEventPath,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("analytics ACK altered default artifact %s: %v", path, err)
		}
	}
	p155WaitRequestConsumed(t, defaultMailbox.Root, requestID)
	p155WaitRequestConsumed(t, analyticsMailbox.Root, requestID)

	finalHealth := p155WaitForReadyHealth(t, localSocket)
	if finalHealth.Readiness != "ready" || finalHealth.Metrics.MailboxBacklog != 0 || !p155CurrentHostRouterReady(finalHealth) {
		t.Fatalf("final Mac ingress health=%+v", finalHealth)
	}
	t.Logf("P155 complete: request_id=%s default_command=%s analytics_command=%s", requestID, defaultResponse.CommandID, analyticsResponse.CommandID)
}

func p155Contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func p155WaitForLoadedLaunchAgent(t *testing.T, label string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last string
	for {
		command := exec.Command("/bin/launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), label))
		if output, err := command.CombinedOutput(); err == nil {
			return
		} else {
			last = strings.TrimSpace(string(output))
		}
		if time.Now().After(deadline) {
			t.Fatalf("LaunchAgent %s did not become loaded: %s", label, last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func p155WaitForOwnedSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for {
		if err := p155OwnedSocketError(path); err == nil {
			return
		} else {
			last = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("service socket %s did not become safe: %v", path, last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func p155OwnedSocketError(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("lstat: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("unsafe mode %v", info.Mode())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("socket is not owned by uid %d", os.Geteuid())
	}
	return nil
}

func p155RequireMailboxTree(t *testing.T, root string) {
	t.Helper()
	for _, child := range []string{"", "inbox", "outbox", "events", "acks", "diagnostics"} {
		path := root
		if child != "" {
			path = filepath.Join(root, child)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("unsafe mailbox directory %s: info=%v err=%v", path, info, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Geteuid() {
			t.Fatalf("mailbox directory %s is not owned by uid %d", path, os.Geteuid())
		}
	}
}

func p155RequireInstalledConfigValidation(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(p155MacServiceRoot, "bin", "runner-local"), "validate-config", "--config", p155MacConfigPath)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "schema_version=2") || !strings.Contains(string(output), "analytics") || !strings.Contains(string(output), "default") {
		t.Fatalf("installed runner-local config validation failed: err=%v output=%s", err, strings.TrimSpace(string(output)))
	}
}

func p155WaitForReadyHealth(t *testing.T, socketPath string) p155HealthReport {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last error
	for {
		report, err := p155ReadHealth(socketPath)
		if err == nil && report.Readiness == "ready" {
			return report
		}
		if err != nil {
			last = err
		} else {
			last = fmt.Errorf("readiness=%q", report.Readiness)
		}
		if time.Now().After(deadline) {
			t.Fatalf("readiness through %s did not become ready: %v", socketPath, last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func p155ReadHealth(socketPath string) (p155HealthReport, error) {
	dialer := &net.Dialer{}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socketPath)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	response, err := client.Get("http://runner/health/ready")
	if err != nil {
		return p155HealthReport{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return p155HealthReport{}, fmt.Errorf("status=%s", response.Status)
	}
	var report p155HealthReport
	if err := json.NewDecoder(response.Body).Decode(&report); err != nil {
		return p155HealthReport{}, err
	}
	return report, nil
}

func p155HealthCheckIs(report p155HealthReport, component, state string) bool {
	for _, check := range report.Checks {
		if check.Component == component && check.State == state {
			return true
		}
	}
	return false
}

// p155CurrentHostRouterReady accepts the original single-profile health name
// and the qualified name emitted once a later P157 profile is configured.
func p155CurrentHostRouterReady(report p155HealthReport) bool {
	return p155HealthCheckIs(report, "remote_router", "ready") ||
		p155HealthCheckIs(report, "remote_router/linux-host", "ready")
}

func TestP155CurrentHostRouterReadySupportsQualifiedHealth(t *testing.T) {
	tests := []struct {
		name   string
		checks []p155HealthCheck
		want   bool
	}{
		{name: "single profile", checks: []p155HealthCheck{{Component: "remote_router", State: "ready"}}, want: true},
		{name: "multiple profiles", checks: []p155HealthCheck{{Component: "remote_router/linux-host", State: "ready"}}, want: true},
		{name: "wrong profile only", checks: []p155HealthCheck{{Component: "remote_router/sandbox-host", State: "ready"}}, want: false},
		{name: "current profile degraded", checks: []p155HealthCheck{{Component: "remote_router/linux-host", State: "degraded"}}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := p155CurrentHostRouterReady(p155HealthReport{Checks: test.checks}); got != test.want {
				t.Fatalf("p155CurrentHostRouterReady()=%t, want %t", got, test.want)
			}
		})
	}
}

func p155Request(t *testing.T, value map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func p155WaitTerminalResponse(t *testing.T, client *mailboxclient.Client, requestID string) mailboxclient.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for {
		response, err := client.WaitResponse(ctx, requestID)
		if err != nil {
			t.Fatalf("wait for %s response: %v", requestID, err)
		}
		switch response.RequestState {
		case "complete", "rejected", "indeterminate":
			return response
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for terminal %s response: %v", requestID, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func p155AssertRunResponse(t *testing.T, response mailboxclient.Response, inboxID, selectionSource, environment, targetKind, targetProfile, expectedOutput string) {
	t.Helper()
	if response.InboxID != inboxID || response.Operation != "run" || response.RequestState != "complete" || response.JobPhase != "complete" ||
		response.CommandID == "" || response.SessionID == "" || response.JobID == "" || response.CommandState != "succeeded" ||
		response.ExitCode == nil || *response.ExitCode != 0 || response.OutputComplete == nil || !*response.OutputComplete ||
		response.OutputTruncated == nil || *response.OutputTruncated || response.OutputUnavailableReason != "" || response.EventsFile == "" ||
		response.ExecutionSelectionSource != selectionSource || response.ResolvedEnvironment != environment ||
		response.ResolvedExecutionTarget == nil || response.ResolvedExecutionTarget.Kind != targetKind || response.ResolvedExecutionTarget.Profile != targetProfile ||
		response.TeardownOutcome != "closed" || response.FinalEventSequence == nil || response.AvailableEventSequence == nil ||
		*response.FinalEventSequence != *response.AvailableEventSequence || !strings.Contains(response.Stdout, expectedOutput) {
		t.Fatalf("unexpected %s run response=%+v", inboxID, response)
	}
}

func p155AssertRemoteHostname(t *testing.T, stdout string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[2]) == "" {
		t.Fatalf("remote run did not report a hostname: %q", stdout)
	}
}

func p155AssertEvents(t *testing.T, client *mailboxclient.Client, root string, response mailboxclient.Response, expectedOutput string) string {
	t.Helper()
	events, err := client.ReadEventsThroughCursor(response)
	if err != nil {
		t.Fatalf("read %s events: %v", response.InboxID, err)
	}
	if len(events) == 0 || response.AvailableEventSequence == nil || *response.AvailableEventSequence == 0 {
		t.Fatalf("%s response advertised no events: %+v", response.InboxID, response)
	}
	var stdout strings.Builder
	for _, event := range events {
		if event.Type == "stdout" {
			if event.Encoding != "utf8" {
				t.Fatalf("%s safe script emitted non-UTF-8 stdout: %+v", response.InboxID, event)
			}
			stdout.WriteString(event.Text)
		}
	}
	if !strings.Contains(stdout.String(), expectedOutput) {
		t.Fatalf("%s event stdout=%q, want %q", response.InboxID, stdout.String(), expectedOutput)
	}
	path := filepath.Join(root, filepath.FromSlash(response.EventsFile))
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("unsafe %s event projection %s: info=%v err=%v", response.InboxID, path, info, err)
	}
	return path
}

func p155WaitAckConsumed(t *testing.T, root, requestID string) {
	t.Helper()
	p155WaitForAbsent(t, []string{
		filepath.Join(root, "acks", requestID+".json"),
		filepath.Join(root, "acks", requestID+".ready"),
	})
}

func p155WaitRequestConsumed(t *testing.T, root, requestID string) {
	t.Helper()
	p155WaitForAbsent(t, []string{
		filepath.Join(root, "inbox", requestID+".json"),
		filepath.Join(root, "inbox", requestID+".ready"),
	})
}

func p155WaitForAbsent(t *testing.T, paths []string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		allAbsent := true
		for _, path := range paths {
			if _, err := os.Lstat(path); err == nil {
				allAbsent = false
				break
			} else if !os.IsNotExist(err) {
				t.Fatalf("inspect mailbox artifact %s: %v", path, err)
			}
		}
		if allAbsent {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("mailbox artifacts remain after 15 seconds: %v", paths)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
