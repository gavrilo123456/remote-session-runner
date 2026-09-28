package runnercli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/runnerclient"
)

func TestP119HelpAndVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runWithDependencies([]string{"--help"}, &stdout, &stderr, cliDependencies{}); code != 0 {
		t.Fatalf("help exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "--endpoint <local|profile>") || stderr.Len() != 0 {
		t.Fatalf("help output=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runWithDependencies([]string{"--version"}, &stdout, &stderr, cliDependencies{}); code != 0 {
		t.Fatalf("version exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "runner 0.0.0-dev") || stderr.Len() != 0 {
		t.Fatalf("version output=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestP119SessionCreateWaitsForReadyAndUsesSelectedEndpoint(t *testing.T) {
	client := &fakeSessionClient{
		kind: runnerclient.EndpointUnixSocket,
		acceptance: runnerclient.Acceptance{
			ResourceID: "session-p119-ready", SessionID: "session-p119-ready",
			AcceptanceScope: "local_intent", ExecutionTarget: runnerclient.Target{Kind: "local", Profile: "mac-workstation"},
		},
		snapshots: []runnerclient.Snapshot[runnerclient.SessionResource]{
			p119Snapshot("creating", false),
			p119Snapshot("ready", false),
		},
	}
	resolver := &fakeEndpointResolver{clients: map[string]sessionClient{"local": client}}
	clock := &testClock{now: time.Now()}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies([]string{
		"--endpoint", "local", "--wait-timeout=3s", "session", "create",
		"--environment", "mac-dev", "--target", "local", "--profile", "mac-workstation",
		"--idempotency-key", "create-key-p119",
	}, &stdout, &stderr, cliDependencies{resolver: resolver, now: clock.Now, sleep: clock.Sleep})
	if code != 0 {
		t.Fatalf("create exit code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if client.createCalls != 1 || client.getCalls != 2 {
		t.Fatalf("client calls: create=%d get=%d, want 1 and 2", client.createCalls, client.getCalls)
	}
	if resolver.requested != "local" || client.createdRequest.Environment != "mac-dev" ||
		client.createdRequest.ExecutionTarget != (runnerclient.Target{Kind: "local", Profile: "mac-workstation"}) || client.createdKey != "create-key-p119" {
		t.Fatalf("endpoint/request/key selection was not preserved: endpoint=%q request=%+v key=%q", resolver.requested, client.createdRequest, client.createdKey)
	}
	for _, want := range []string{"session_id: session-p119-ready", "acceptance_scope: local_intent", "session_state: ready"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout %q does not contain %q", stdout.String(), want)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q, want empty", stderr.String())
	}
}

func TestP119NoWaitReturnsAcceptedIDWithoutPolling(t *testing.T) {
	client := &fakeSessionClient{
		kind: runnerclient.EndpointUnixSocket,
		acceptance: runnerclient.Acceptance{
			ResourceID: "session-p119-no-wait", SessionID: "session-p119-no-wait",
			AcceptanceScope: "local_intent", ExecutionTarget: runnerclient.Target{Kind: "remote", Profile: "linux-host"},
			KnownState: runnerclient.KnownState{DeliveryState: "recorded"},
		},
	}
	resolver := &fakeEndpointResolver{clients: map[string]sessionClient{"local": client}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies([]string{
		"--endpoint", "local", "session", "create", "--environment", "linux-dev",
		"--target", "remote", "--profile", "linux-host", "--no-wait",
	}, &stdout, &stderr, cliDependencies{resolver: resolver})
	if code != 0 {
		t.Fatalf("create --no-wait exit code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if client.createCalls != 1 || client.getCalls != 0 {
		t.Fatalf("client calls: create=%d get=%d, want 1 and 0", client.createCalls, client.getCalls)
	}
	if !strings.Contains(stdout.String(), "session_id: session-p119-no-wait") || !strings.Contains(stdout.String(), "readiness: pending") {
		t.Fatalf("stdout does not retain the accepted pending ID: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q, want empty", stderr.String())
	}
}

func TestP119ReadinessTimeoutRetainsSessionIDWithoutCancelling(t *testing.T) {
	client := &fakeSessionClient{
		kind: runnerclient.EndpointUnixSocket,
		acceptance: runnerclient.Acceptance{
			ResourceID: "session-p119-pending", SessionID: "session-p119-pending",
			AcceptanceScope: "local_intent", ExecutionTarget: runnerclient.Target{Kind: "local", Profile: "mac-workstation"},
		},
		snapshots:  []runnerclient.Snapshot[runnerclient.SessionResource]{p119Snapshot("creating", false)},
		repeatLast: true,
	}
	resolver := &fakeEndpointResolver{clients: map[string]sessionClient{"local": client}}
	clock := &testClock{now: time.Now()}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies([]string{
		"--endpoint", "local", "--wait-timeout=1s", "session", "create",
		"--environment", "mac-dev", "--target", "local", "--profile", "mac-workstation",
	}, &stdout, &stderr, cliDependencies{resolver: resolver, now: clock.Now, sleep: clock.Sleep})
	if code != exitWaitPending {
		t.Fatalf("create timeout exit code=%d stdout=%q stderr=%q, want %d", code, stdout.String(), stderr.String(), exitWaitPending)
	}
	if client.createCalls != 1 || client.getCalls != 4 {
		t.Fatalf("client calls: create=%d get=%d, want 1 and 4", client.createCalls, client.getCalls)
	}
	for _, want := range []string{"session_id: session-p119-pending", "wait timed out", "was not cancelled"} {
		if !strings.Contains(stdout.String()+stderr.String(), want) {
			t.Errorf("combined output %q does not contain %q", stdout.String()+stderr.String(), want)
		}
	}
}

func TestP119ReadinessReadFailureRetainsAcceptedIDWithoutCancelling(t *testing.T) {
	client := &fakeSessionClient{
		kind:       runnerclient.EndpointHTTPS,
		acceptance: runnerclient.Acceptance{ResourceID: "session-p119-read-error", SessionID: "session-p119-read-error", AcceptanceScope: "target_authority", ExecutionTarget: runnerclient.Target{Kind: "remote", Profile: "linux-host"}},
		getErr:     errors.New("temporary status read failure"),
	}
	resolver := &fakeEndpointResolver{clients: map[string]sessionClient{"linux-poc": client}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies([]string{
		"--endpoint", "linux-poc", "session", "create", "--environment", "linux-dev",
		"--target", "remote", "--profile", "linux-host", "--idempotency-key", "p119-read-error",
	}, &stdout, &stderr, cliDependencies{resolver: resolver})
	if code != 1 || client.createCalls != 1 || client.getCalls != 1 {
		t.Fatalf("create read failure exit=%d create=%d get=%d stdout=%q stderr=%q", code, client.createCalls, client.getCalls, stdout.String(), stderr.String())
	}
	for _, want := range []string{"session_id: session-p119-read-error", "was not cancelled", "session status session-p119-read-error"} {
		if !strings.Contains(stdout.String()+stderr.String(), want) {
			t.Errorf("combined output %q does not contain %q", stdout.String()+stderr.String(), want)
		}
	}
}

func TestP119StatusDisplaysTargetAuthorityAndEffectiveCapabilities(t *testing.T) {
	resource := p119Snapshot("ready", true).Resource
	resource.Capabilities = runnerclient.Capabilities{
		HostClass: "macOS workstation", EffectiveAccount: "tomasz.walczuk", Isolation: "os-user",
		ServiceLimits: map[string]any{"active_sessions_per_host": float64(20), "command_timeout": "30m"},
	}
	resource.ExecutionTarget = runnerclient.Target{Kind: "remote", Profile: "linux-host"}
	resource.Authority = "remote"
	resource.Environment = "linux-dev"
	client := &fakeSessionClient{kind: runnerclient.EndpointUnixSocket, snapshots: []runnerclient.Snapshot[runnerclient.SessionResource]{{View: "projection", IsStale: true, Resource: resource}}}
	resolver := &fakeEndpointResolver{clients: map[string]sessionClient{"local": client}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies([]string{"--endpoint", "local", "session", "status", "opaque-session-id"}, &stdout, &stderr, cliDependencies{resolver: resolver})
	if code != 0 {
		t.Fatalf("status exit code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"session_id: session-p119-ready", "execution_target: remote/linux-host", "host_class: macOS workstation", "effective_account: tomasz.walczuk",
		"isolation: os-user", "service_limit.active_sessions_per_host: 20", "is_stale: true",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout %q does not contain %q", stdout.String(), want)
		}
	}
	if resolver.requested != "local" || client.lastSessionID != "opaque-session-id" {
		t.Fatalf("status changed ingress based on ID: endpoint=%q id=%q", resolver.requested, client.lastSessionID)
	}
}

func TestP119ExplicitEndpointRequiredAndHTTPSRejectsLocalTarget(t *testing.T) {
	resolver := &fakeEndpointResolver{clients: map[string]sessionClient{}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies([]string{"session", "status", "opaque"}, &stdout, &stderr, cliDependencies{resolver: resolver})
	if code != exitInvalidInvocation || resolver.calls != 0 {
		t.Fatalf("missing endpoint exit=%d resolver calls=%d stderr=%q", code, resolver.calls, stderr.String())
	}

	client := &fakeSessionClient{kind: runnerclient.EndpointHTTPS}
	resolver.clients["linux-poc"] = client
	stdout.Reset()
	stderr.Reset()
	code = runWithDependencies([]string{
		"--endpoint", "linux-poc", "session", "create", "--environment", "mac-dev",
		"--target", "local", "--profile", "mac-workstation",
	}, &stdout, &stderr, cliDependencies{resolver: resolver})
	if code != exitInvalidInvocation || client.createCalls != 0 {
		t.Fatalf("HTTPS local-target exit=%d create calls=%d stderr=%q", code, client.createCalls, stderr.String())
	}
}

func TestP119TerminalCreateOutcomeIsReportedWithStableID(t *testing.T) {
	failed := p119Snapshot("failed", false)
	failed.Resource.SessionID = "session-p119-failed"
	client := &fakeSessionClient{
		kind:       runnerclient.EndpointHTTPS,
		acceptance: runnerclient.Acceptance{ResourceID: "session-p119-failed", SessionID: "session-p119-failed", AcceptanceScope: "target_authority", ExecutionTarget: runnerclient.Target{Kind: "remote", Profile: "linux-host"}},
		snapshots:  []runnerclient.Snapshot[runnerclient.SessionResource]{failed},
	}
	resolver := &fakeEndpointResolver{clients: map[string]sessionClient{"linux-poc": client}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies([]string{
		"--endpoint", "linux-poc", "session", "create", "--environment", "linux-dev",
		"--target", "remote", "--profile", "linux-host", "--idempotency-key", "p119-failure",
	}, &stdout, &stderr, cliDependencies{resolver: resolver})
	if code != 1 || !strings.Contains(stdout.String(), "session_id: session-p119-failed") || !strings.Contains(stderr.String(), "failed") {
		t.Fatalf("terminal create result exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

type fakeEndpointResolver struct {
	clients   map[string]sessionClient
	requested string
	calls     int
}

func (r *fakeEndpointResolver) Resolve(name, _ string) (sessionClient, error) {
	r.calls++
	r.requested = name
	client, ok := r.clients[name]
	if !ok {
		return nil, errors.New("profile unavailable")
	}
	return client, nil
}

type fakeSessionClient struct {
	kind           runnerclient.EndpointKind
	acceptance     runnerclient.Acceptance
	createdRequest runnerclient.CreateSessionRequest
	createdKey     string
	createErr      error
	snapshots      []runnerclient.Snapshot[runnerclient.SessionResource]
	repeatLast     bool
	getErr         error
	createCalls    int
	getCalls       int
	lastSessionID  string
}

func (c *fakeSessionClient) EndpointKind() runnerclient.EndpointKind { return c.kind }

func (c *fakeSessionClient) CreateSession(_ context.Context, request runnerclient.CreateSessionRequest, key string) (runnerclient.Acceptance, error) {
	c.createCalls++
	c.createdRequest = request
	c.createdKey = key
	return c.acceptance, c.createErr
}

func (c *fakeSessionClient) GetSession(_ context.Context, id string) (runnerclient.Snapshot[runnerclient.SessionResource], error) {
	c.getCalls++
	c.lastSessionID = id
	if c.getErr != nil {
		return runnerclient.Snapshot[runnerclient.SessionResource]{}, c.getErr
	}
	if len(c.snapshots) == 0 {
		return runnerclient.Snapshot[runnerclient.SessionResource]{}, errors.New("unexpected session read")
	}
	if c.repeatLast && len(c.snapshots) == 1 {
		return c.snapshots[0], nil
	}
	snapshot := c.snapshots[0]
	c.snapshots = c.snapshots[1:]
	return snapshot, nil
}

func p119Snapshot(state string, stale bool) runnerclient.Snapshot[runnerclient.SessionResource] {
	return runnerclient.Snapshot[runnerclient.SessionResource]{
		View:    "authority",
		IsStale: stale,
		Resource: runnerclient.SessionResource{
			SessionID: "session-p119-ready", SessionState: state,
			ExecutionTarget: runnerclient.Target{Kind: "local", Profile: "mac-workstation"},
			Environment:     "mac-dev", Authority: "local", Source: runnerclient.Source{Mode: "empty"},
		},
	}
}

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

func (c *testClock) Sleep(_ context.Context, duration time.Duration) error {
	c.now = c.now.Add(duration)
	return nil
}
