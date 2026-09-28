package runnercli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	if !strings.Contains(stdout.String(), "--endpoint <local|profile>") || !strings.Contains(stdout.String(), "exec") || !strings.Contains(stdout.String(), "events") || stderr.Len() != 0 {
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

func TestP120ExecStreamsRawOutputAndReturnsCommandExit(t *testing.T) {
	for _, test := range []struct {
		name     string
		state    string
		exitCode int
		terminal string
		wantExit int
	}{
		{name: "success", state: "succeeded", exitCode: 0, terminal: "command_succeeded", wantExit: 0},
		{name: "shell nonzero", state: "failed", exitCode: 7, terminal: "command_failed", wantExit: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			resource := p120CommandResource(test.state, test.exitCode, 4)
			client := &fakeCommandClient{
				fakeSessionClient: &fakeSessionClient{kind: runnerclient.EndpointHTTPS},
				acceptance:        runnerclient.Acceptance{ResourceID: "command-p120", SessionID: "session-p120", CommandID: "command-p120", AcceptanceScope: "target_authority"},
				snapshot:          runnerclient.Snapshot[runnerclient.CommandResource]{View: "authority", Resource: resource},
			}
			resolver := &fakeEndpointResolver{clients: map[string]sessionClient{"linux-poc": client}}
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			rawOutput := []byte{0xff, 0x00, 'o', 'k'}
			streams := 0
			dependencies := cliDependencies{
				resolver: resolver,
				openEvents: func(_ context.Context, got sessionClient, commandID string, after int64, follow bool) (commandEventStream, error) {
					streams++
					if got != client || commandID != "command-p120" || after != 0 || !follow {
						t.Fatalf("event endpoint/id/cursor/follow changed: got=%T id=%q after=%d follow=%t", got, commandID, after, follow)
					}
					if !strings.Contains(stderr.String(), "command_id: command-p120\n") {
						t.Fatal("accepted command ID was not displayed before opening the event stream")
					}
					return &fakeP120EventStream{events: []runnerclient.Event{
						p120Event("command-p120", 1, "command_queued", nil, nil),
						p120Event("command-p120", 2, "stdout", rawOutput, nil),
						p120Event("command-p120", 3, "stderr", []byte("err\n"), nil),
						p120Event("command-p120", 4, test.terminal, nil, &test.exitCode),
					}}, nil
				},
			}
			code := runWithDependencies([]string{"--endpoint", "linux-poc", "exec", "--idempotency-key", "p120-key", "session-p120", "--", "printf 'hello world'"}, stdout, stderr, dependencies)
			if code != test.wantExit {
				t.Fatalf("exec exit=%d want=%d stdout=%q stderr=%q", code, test.wantExit, stdout.Bytes(), stderr.String())
			}
			if resolver.requested != "linux-poc" || client.submitCalls != 1 || client.submitKey != "p120-key" || client.submitSession != "session-p120" || client.submitRequest.Script != "printf 'hello world'" || streams != 1 {
				t.Fatalf("selected endpoint or submitted command changed: endpoint=%q submits=%d key=%q session=%q script=%q streams=%d", resolver.requested, client.submitCalls, client.submitKey, client.submitSession, client.submitRequest.Script, streams)
			}
			if !bytes.Equal(stdout.Bytes(), rawOutput) || !bytes.Contains(stderr.Bytes(), []byte("err\n")) {
				t.Fatalf("stdout contains metadata or lost raw command output bytes: %v", stdout.Bytes())
			}
			if !strings.Contains(stderr.String(), "command_id: command-p120\n") || !strings.Contains(stderr.String(), "event_cursor: 4") {
				t.Fatalf("stderr lost accepted ID or command cursor: %q", stderr.String())
			}
			if !strings.Contains(stderr.String(), "idempotency_key: p120-key") || !strings.Contains(stderr.String(), "event: sequence=4 type="+test.terminal) {
				t.Fatalf("stderr lacks retry key or lifecycle event: %q", stderr.String())
			}
		})
	}
}

func TestP120ExecTransportFailureKeepsAcceptedIDAndResumeCursor(t *testing.T) {
	client := &fakeCommandClient{
		fakeSessionClient: &fakeSessionClient{kind: runnerclient.EndpointUnixSocket},
		acceptance:        runnerclient.Acceptance{ResourceID: "command-p120-drop", SessionID: "session-p120-drop", CommandID: "command-p120-drop", AcceptanceScope: "local_intent"},
	}
	resolver := &fakeEndpointResolver{clients: map[string]sessionClient{"local": client}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies([]string{"--endpoint", "local", "exec", "session-p120-drop", "--", "sleep 1"}, &stdout, &stderr, cliDependencies{
		resolver: resolver,
		openEvents: func(_ context.Context, _ sessionClient, _ string, after int64, follow bool) (commandEventStream, error) {
			if after != 0 || !follow {
				t.Fatalf("initial stream cursor/follow = %d/%t", after, follow)
			}
			return &fakeP120EventStream{events: []runnerclient.Event{p120Event("command-p120-drop", 1, "command_queued", nil, nil)}, err: io.ErrUnexpectedEOF}, nil
		},
	})
	if code != 1 || !strings.Contains(stderr.String(), "command_id: command-p120-drop") || !strings.Contains(stderr.String(), "events command-p120-drop --after 1 --follow") || !strings.Contains(stderr.String(), "was not cancelled") {
		t.Fatalf("transport failure lost ID or resume cursor: exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestP120EventsFollowsFromValidatedCursorAndShowsCompleteness(t *testing.T) {
	final := int64(2)
	exitCode := 0
	resource := runnerclient.CommandResource{
		CommandID: "command-p120-follow", SessionID: "session-p120-follow", CommandState: "succeeded",
		ExitCode: &exitCode, FinalEventSequence: &final, OutputComplete: true,
	}
	client := &fakeCommandClient{
		fakeSessionClient: &fakeSessionClient{kind: runnerclient.EndpointHTTPS},
		snapshot:          runnerclient.Snapshot[runnerclient.CommandResource]{View: "authority", Resource: resource},
		snapshots: []runnerclient.Snapshot[runnerclient.CommandResource]{
			{View: "authority", Resource: runnerclient.CommandResource{CommandID: "command-p120-follow", CommandState: "running"}},
			{View: "authority", Resource: resource},
		},
	}
	resolver := &fakeEndpointResolver{clients: map[string]sessionClient{"linux-poc": client}}
	var stdout, stderr bytes.Buffer
	var cursors []int64
	streams := 0
	code := runWithDependencies([]string{"--endpoint", "linux-poc", "events", "command-p120-follow", "--after", "0", "--follow"}, &stdout, &stderr, cliDependencies{
		resolver: resolver,
		openEvents: func(_ context.Context, _ sessionClient, commandID string, after int64, follow bool) (commandEventStream, error) {
			if commandID != "command-p120-follow" || !follow {
				t.Fatalf("event identity/follow changed: id=%q follow=%t", commandID, follow)
			}
			cursors = append(cursors, after)
			streams++
			if streams == 1 {
				return &fakeP120EventStream{events: []runnerclient.Event{p120Event(commandID, 1, "command_queued", nil, nil)}}, nil
			}
			return &fakeP120EventStream{events: []runnerclient.Event{p120Event(commandID, 2, "command_succeeded", nil, &exitCode)}}, nil
		},
		sleep: func(context.Context, time.Duration) error { return nil },
	})
	if code != 0 || len(cursors) != 2 || cursors[0] != 0 || cursors[1] != 1 {
		t.Fatalf("follow did not resume at the last validated cursor: exit=%d cursors=%v stdout=%q stderr=%q", code, cursors, stdout.String(), stderr.String())
	}
	for _, want := range []string{"event_cursor: 2", "final_event_sequence: 2", "output_complete: true", "output_truncated: false", "event_history_complete_this_read: true"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr %q does not contain %q", stderr.String(), want)
		}
	}
}

func TestP120EventsFiniteReplayKeepsExplicitEndpointAndRequestedCursor(t *testing.T) {
	final := int64(8)
	resource := runnerclient.CommandResource{
		CommandID: "command-p120-finite", SessionID: "session-p120-finite", CommandState: "running",
		FinalEventSequence: &final, OutputComplete: true,
	}
	client := &fakeCommandClient{
		fakeSessionClient: &fakeSessionClient{kind: runnerclient.EndpointHTTPS},
		snapshot:          runnerclient.Snapshot[runnerclient.CommandResource]{View: "projection", IsStale: true, Resource: resource},
	}
	resolver := &fakeEndpointResolver{clients: map[string]sessionClient{"linux-poc": client}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies([]string{"--endpoint", "linux-poc", "events", "command-p120-finite", "--after=7"}, &stdout, &stderr, cliDependencies{
		resolver: resolver,
		openEvents: func(_ context.Context, selected sessionClient, commandID string, after int64, follow bool) (commandEventStream, error) {
			if selected != client || commandID != "command-p120-finite" || after != 7 || follow {
				t.Fatalf("finite read changed endpoint/id/cursor/follow: selected=%T id=%q after=%d follow=%t", selected, commandID, after, follow)
			}
			return &fakeP120EventStream{events: []runnerclient.Event{p120Event(commandID, 8, "stdout", []byte("tail"), nil)}}, nil
		},
	})
	if code != 0 || resolver.requested != "linux-poc" || !bytes.Equal(stdout.Bytes(), []byte("tail")) {
		t.Fatalf("finite replay exit=%d endpoint=%q stdout=%v stderr=%q", code, resolver.requested, stdout.Bytes(), stderr.String())
	}
	for _, want := range []string{"event_cursor: 8", "final_event_sequence: 8", "output_complete: true", "event_history_complete_this_read: false", "is_stale: true"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr %q does not contain %q", stderr.String(), want)
		}
	}
}

func TestP120EventsReportsExpiredHistoryAsIncomplete(t *testing.T) {
	client := &fakeCommandClient{fakeSessionClient: &fakeSessionClient{kind: runnerclient.EndpointUnixSocket}}
	resolver := &fakeEndpointResolver{clients: map[string]sessionClient{"local": client}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies([]string{"--endpoint", "local", "events", "command-p120-expired", "--after=4"}, &stdout, &stderr, cliDependencies{
		resolver: resolver,
		openEvents: func(_ context.Context, _ sessionClient, _ string, after int64, follow bool) (commandEventStream, error) {
			if after != 4 || follow {
				t.Fatalf("finite resume requested after=%d follow=%t", after, follow)
			}
			return nil, &runnerclient.APIError{
				StatusCode: 410, Code: "event_history_unavailable", Message: "retention expired",
				Details: json.RawMessage(`{"output_complete":false,"output_unavailable_reason":"retention_expired","earliest_available_sequence":12}`),
			}
		},
	})
	if code != 1 || !strings.Contains(stderr.String(), "output_complete: false") || !strings.Contains(stderr.String(), "output_unavailable_reason: retention_expired") || !strings.Contains(stderr.String(), "event_cursor: 4") || !strings.Contains(stderr.String(), "earliest_available_sequence: 12") || !strings.Contains(stderr.String(), "events command-p120-expired --after 11 --follow") || !strings.Contains(stderr.String(), "complete output remains unavailable") {
		t.Fatalf("expired history was not reported as incomplete/resumable: exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func p120CommandResource(state string, exitCode, finalSequence int) runnerclient.CommandResource {
	final := int64(finalSequence)
	return runnerclient.CommandResource{
		CommandID: "command-p120", SessionID: "session-p120", CommandState: state,
		ExitCode: &exitCode, FinalEventSequence: &final, OutputComplete: true,
	}
}

func p120Event(commandID string, sequence int64, eventType string, data []byte, exitCode *int) runnerclient.Event {
	return runnerclient.Event{CommandID: commandID, Sequence: sequence, Type: eventType, Data: data, ExitCode: exitCode}
}

type fakeCommandClient struct {
	*fakeSessionClient
	acceptance    runnerclient.Acceptance
	submitErr     error
	submitCalls   int
	submitKey     string
	submitSession string
	submitRequest runnerclient.SubmitCommandRequest
	snapshot      runnerclient.Snapshot[runnerclient.CommandResource]
	snapshots     []runnerclient.Snapshot[runnerclient.CommandResource]
	getCalls      int
	getErr        error
}

func (c *fakeCommandClient) SubmitCommand(_ context.Context, sessionID string, request runnerclient.SubmitCommandRequest, key string) (runnerclient.Acceptance, error) {
	c.submitCalls++
	c.submitSession, c.submitRequest, c.submitKey = sessionID, request, key
	return c.acceptance, c.submitErr
}

func (c *fakeCommandClient) GetCommand(_ context.Context, _ string) (runnerclient.Snapshot[runnerclient.CommandResource], error) {
	c.getCalls++
	if c.getErr != nil {
		return runnerclient.Snapshot[runnerclient.CommandResource]{}, c.getErr
	}
	if len(c.snapshots) > 0 {
		snapshot := c.snapshots[0]
		c.snapshots = c.snapshots[1:]
		return snapshot, nil
	}
	return c.snapshot, nil
}

type fakeP120EventStream struct {
	events []runnerclient.Event
	err    error
	cursor int64
	closed bool
}

func (s *fakeP120EventStream) Next() (runnerclient.Event, error) {
	if len(s.events) != 0 {
		event := s.events[0]
		s.events = s.events[1:]
		s.cursor = event.Sequence
		return event, nil
	}
	if s.err != nil {
		err := s.err
		s.err = nil
		return runnerclient.Event{}, err
	}
	return runnerclient.Event{}, io.EOF
}

func (s *fakeP120EventStream) Cursor() int64 { return s.cursor }

func (s *fakeP120EventStream) Close() error {
	s.closed = true
	return nil
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
