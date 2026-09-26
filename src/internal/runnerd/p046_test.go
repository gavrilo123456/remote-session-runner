package runnerd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	hostruntime "remote-session-runner/src/internal/runtime"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

type p046FakeRuntime struct {
	generation   string
	commandCalls int
}

func (r *p046FakeRuntime) Prepare(context.Context, execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	return execution.RuntimePrepared{RuntimeGeneration: r.generation}, nil
}

func (r *p046FakeRuntime) StartAgent(context.Context, execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	return execution.RuntimeStarted{RuntimeGeneration: r.generation}, nil
}

func (*p046FakeRuntime) Cleanup(context.Context, execution.RuntimeCleanupRequest) error { return nil }

func (r *p046FakeRuntime) ExecuteCommand(context.Context, execution.RuntimeCommandRequest) (execution.RuntimeCommandResult, error) {
	r.commandCalls++
	return execution.RuntimeCommandResult{Stdout: []byte("p047-output\n"), ExitCode: 0}, nil
}

func TestP046PrivateCreateReadOwnerOnlySocket(t *testing.T) {
	service, authority := newP046Service(t, &p046FakeRuntime{generation: "p046-fake-generation"})
	socketParent := p046SocketParent(t)
	socketPath := filepath.Join(socketParent, "runnerd.sock")
	server, err := NewPrivateServer(PrivateServerOptions{Service: service, SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	defer func() {
		if err := server.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := <-serveErr; err != nil {
			t.Fatal(err)
		}
	}()

	socketInfo, err := os.Stat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := socketInfo.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("socket mode = %o, want %o", got, want)
	}
	parentInfo, err := os.Stat(socketParent)
	if err != nil {
		t.Fatal(err)
	}
	if parentInfo.Mode().Perm()&0o077 != 0 {
		t.Fatalf("socket parent mode = %o, has group/other access", parentInfo.Mode().Perm())
	}

	client := p046UnixClient(socketPath)
	body := []byte(`{"session_id":"p046-session","idempotency_key":"p046-key","request_id":"p046-request","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"queued_mac","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"}}`)
	response := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions", body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", response.StatusCode, p046ReadBody(t, response))
	}
	var created map[string]any
	p046DecodeJSON(t, response, &created)
	if created["session_id"] != "p046-session" || created["session_state"] != string(domain.SessionStateReady) || created["runtime_generation"] != "p046-fake-generation" {
		t.Fatalf("create response = %#v", created)
	}

	duplicate := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions", body)
	if duplicate.StatusCode != http.StatusAccepted {
		t.Fatalf("duplicate status = %d, body = %s", duplicate.StatusCode, p046ReadBody(t, duplicate))
	}
	var duplicateResource map[string]any
	p046DecodeJSON(t, duplicate, &duplicateResource)
	if duplicateResource["duplicate"] != true {
		t.Fatalf("duplicate response = %#v", duplicateResource)
	}

	readURL := "http://runnerd/internal/v1/sessions/p046-session?controller_type=queued_mac&controller_id=tomasz.walczuk"
	read := p046DoJSON(t, client, http.MethodGet, readURL, nil)
	if read.StatusCode != http.StatusOK {
		t.Fatalf("read status = %d, body = %s", read.StatusCode, p046ReadBody(t, read))
	}
	var readResource map[string]any
	p046DecodeJSON(t, read, &readResource)
	if readResource["session_id"] != "p046-session" || readResource["session_state"] != string(domain.SessionStateReady) {
		t.Fatalf("read response = %#v", readResource)
	}

	wrongController := p046DoJSON(t, client, http.MethodGet, "http://runnerd/internal/v1/sessions/p046-session?controller_type=direct_mtls&controller_id=tomasz.walczuk", nil)
	if wrongController.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong controller status = %d, body = %s", wrongController.StatusCode, p046ReadBody(t, wrongController))
	}

	if _, err := authority.GetSession(context.Background(), domain.SessionID("p046-session")); err != nil {
		t.Fatalf("authoritative read after API operations: %v", err)
	}
}

func TestP046LinuxPrivateCreateReadUsesUbuntuRuntime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real Linux host-process gate runs on Ubuntu")
	}
	fixture := testfixture.New(t)
	workspaceRoot := filepath.Join(fixture.Path(), "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	adapter, err := hostruntime.NewLinuxProcessAdapter(hostruntime.LinuxRuntimeOptions{
		Account:       hostruntime.LinuxHostAccount,
		WorkspaceRoot: workspaceRoot,
		ShellPath:     "/usr/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeAdapter, err := NewLinuxSessionRuntime(adapter)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := newP046Service(t, runtimeAdapter)
	socketParent := p046SocketParent(t)
	socketPath := filepath.Join(socketParent, "runnerd.sock")
	server, err := NewPrivateServer(PrivateServerOptions{Service: service, SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	defer func() {
		_ = server.Close(context.Background())
		if err := <-serveErr; err != nil {
			t.Error(err)
		}
	}()

	sessionID := "p046-linux-session"
	body := []byte(`{"session_id":"p046-linux-session","idempotency_key":"p046-linux-key","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"}}`)
	client := p046UnixClient(socketPath)
	response := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions", body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("Linux create status = %d, body = %s", response.StatusCode, p046ReadBody(t, response))
	}
	var resource sessionResponse
	p046DecodeJSON(t, response, &resource)
	if resource.SessionID != sessionID || resource.SessionState != string(domain.SessionStateReady) || resource.RuntimeGeneration == "" {
		t.Fatalf("Linux create response = %+v", resource)
	}
	process, err := adapter.Inspect(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if process.Username != hostruntime.LinuxHostAccount || process.UID <= 0 {
		t.Fatalf("Linux process identity = %+v", process)
	}
	// Establish one clean command boundary before the adapter cleanup. The
	// P046 API gate is create/read; this direct no-op only proves the ready
	// agent can be closed through the existing P040 lifecycle contract.
	shell, err := adapter.Shell(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shell.RunScript(context.Background(), "p046-cleanup", []byte(":")); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Cleanup(sessionID); err != nil {
		t.Fatal(err)
	}
}

func TestP047PrivateSubmitReadEventsAndReplayAfterServerRestart(t *testing.T) {
	runtimeAdapter := &p046FakeRuntime{generation: "p047-generation"}
	service, authority := newP046Service(t, runtimeAdapter)
	startServer := func() (*PrivateServer, chan error) {
		socketPath := filepath.Join(p046SocketParent(t), "runnerd.sock")
		server, err := NewPrivateServer(PrivateServerOptions{Service: service, SocketPath: socketPath})
		if err != nil {
			t.Fatal(err)
		}
		if err := server.Listen(); err != nil {
			t.Fatal(err)
		}
		serveErr := make(chan error, 1)
		go func() { serveErr <- server.Serve() }()
		return server, serveErr
	}
	server, serveErr := startServer()
	client := p046UnixClient(server.SocketPath())
	firstServerClosed := false
	defer func() {
		if firstServerClosed {
			return
		}
		_ = server.Close(context.Background())
		if err := <-serveErr; err != nil {
			t.Error(err)
		}
	}()

	createBody := []byte(`{"session_id":"p047-session","idempotency_key":"p047-create","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"queued_mac","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"}}`)
	create := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions", createBody)
	if create.StatusCode != http.StatusAccepted {
		t.Fatalf("P047 create status = %d, body = %s", create.StatusCode, p046ReadBody(t, create))
	}
	_ = p046ReadBody(t, create)

	commandBody := []byte(`{"command_id":"p047-command","session_id":"p047-session","idempotency_key":"p047-submit","controller":{"controller_type":"queued_mac","controller_id":"tomasz.walczuk"},"script":"printf p047"}`)
	submit := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions/p047-session/commands", commandBody)
	if submit.StatusCode != http.StatusAccepted {
		t.Fatalf("P047 submit status = %d, body = %s", submit.StatusCode, p046ReadBody(t, submit))
	}
	var submitted commandResponse
	p046DecodeJSON(t, submit, &submitted)
	if submitted.CommandState != string(domain.CommandStateSucceeded) || submitted.ScriptByteCount != len("printf p047") || submitted.ScriptSHA256 == "" {
		t.Fatalf("P047 submit response = %+v", submitted)
	}

	read := p046DoJSON(t, client, http.MethodGet, "http://runnerd/internal/v1/commands/p047-command?controller_type=queued_mac&controller_id=tomasz.walczuk", nil)
	if read.StatusCode != http.StatusOK {
		t.Fatalf("P047 command read status = %d, body = %s", read.StatusCode, p046ReadBody(t, read))
	}
	var readCommand commandResponse
	p046DecodeJSON(t, read, &readCommand)
	if readCommand.ScriptSHA256 != submitted.ScriptSHA256 || readCommand.CommandState != string(domain.CommandStateSucceeded) {
		t.Fatalf("P047 command read = %+v, submit = %+v", readCommand, submitted)
	}

	events := p046DoJSON(t, client, http.MethodGet, "http://runnerd/internal/v1/commands/p047-command/events?controller_type=queued_mac&controller_id=tomasz.walczuk&after=0", nil)
	if events.StatusCode != http.StatusOK {
		t.Fatalf("P047 events status = %d, body = %s", events.StatusCode, p046ReadBody(t, events))
	}
	eventBytes, err := io.ReadAll(events.Body)
	events.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"command_queued", "command_started", "stdout", "command_succeeded", "cDA0Ny1vdXRwdXQK"} {
		if !strings.Contains(string(eventBytes), expected) {
			t.Fatalf("P047 events missing %q: %s", expected, eventBytes)
		}
	}

	duplicate := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions/p047-session/commands", commandBody)
	if duplicate.StatusCode != http.StatusAccepted {
		t.Fatalf("P047 duplicate status = %d, body = %s", duplicate.StatusCode, p046ReadBody(t, duplicate))
	}
	var duplicateCommand commandResponse
	p046DecodeJSON(t, duplicate, &duplicateCommand)
	if !duplicateCommand.Duplicate || duplicateCommand.ScriptSHA256 != submitted.ScriptSHA256 {
		t.Fatalf("P047 duplicate response = %+v", duplicateCommand)
	}

	if err := server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
	firstServerClosed = true
	// A fresh service/runtime instance shares the same authority. The retained
	// idempotency row returns the exact command and script metadata without a
	// second runtime execution.
	newRuntime := &p046FakeRuntime{generation: "p047-new-generation"}
	newService, err := execution.NewExecutionService(authority, newRuntime, mustP047Registry(t), execution.RealClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	service = newService
	server, serveErr = startServer()
	client = p046UnixClient(server.SocketPath())
	defer func() {
		_ = server.Close(context.Background())
		if err := <-serveErr; err != nil {
			t.Error(err)
		}
	}()
	replayed := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions/p047-session/commands", commandBody)
	if replayed.StatusCode != http.StatusAccepted {
		t.Fatalf("P047 restart replay status = %d, body = %s", replayed.StatusCode, p046ReadBody(t, replayed))
	}
	var replayedCommand commandResponse
	p046DecodeJSON(t, replayed, &replayedCommand)
	if !replayedCommand.Duplicate || replayedCommand.ScriptSHA256 != submitted.ScriptSHA256 || newRuntime.commandCalls != 0 {
		t.Fatalf("P047 restart replay response = %+v", replayedCommand)
	}
}

func TestP047LinuxPrivateSubmitReadEventsUsesUbuntuRuntime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real Linux host-process gate runs on Ubuntu")
	}
	fixture := testfixture.New(t)
	workspaceRoot := filepath.Join(fixture.Path(), "workspaces")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	adapter, err := hostruntime.NewLinuxProcessAdapter(hostruntime.LinuxRuntimeOptions{
		Account:       hostruntime.LinuxHostAccount,
		WorkspaceRoot: workspaceRoot,
		ShellPath:     "/usr/bin/bash",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeAdapter, err := NewLinuxSessionRuntime(adapter)
	if err != nil {
		t.Fatal(err)
	}
	service, _ := newP046Service(t, runtimeAdapter)
	socketParent := p046SocketParent(t)
	socketPath := filepath.Join(socketParent, "runnerd.sock")
	server, err := NewPrivateServer(PrivateServerOptions{Service: service, SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	defer func() {
		_ = server.Close(context.Background())
		if err := <-serveErr; err != nil {
			t.Error(err)
		}
		if err := adapter.Cleanup("p047-linux-session"); err != nil {
			t.Error(err)
		}
	}()

	client := p046UnixClient(socketPath)
	createBody := []byte(`{"session_id":"p047-linux-session","idempotency_key":"p047-linux-create","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"}}`)
	create := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions", createBody)
	if create.StatusCode != http.StatusAccepted {
		t.Fatalf("P047 Linux create status = %d, body = %s", create.StatusCode, p046ReadBody(t, create))
	}
	var created sessionResponse
	p046DecodeJSON(t, create, &created)
	if created.SessionState != string(domain.SessionStateReady) || created.RuntimeGeneration == "" {
		t.Fatalf("P047 Linux create response = %+v", created)
	}
	process, err := adapter.Inspect("p047-linux-session")
	if err != nil {
		t.Fatal(err)
	}
	if process.Username != hostruntime.LinuxHostAccount || process.UID <= 0 {
		t.Fatalf("P047 Linux process identity = %+v", process)
	}

	commandBody := []byte(`{"command_id":"p047-linux-command","session_id":"p047-linux-session","idempotency_key":"p047-linux-submit","controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"},"script":"printf p047-linux"}`)
	submit := p046DoJSON(t, client, http.MethodPost, "http://runnerd/internal/v1/sessions/p047-linux-session/commands", commandBody)
	if submit.StatusCode != http.StatusAccepted {
		t.Fatalf("P047 Linux submit status = %d, body = %s", submit.StatusCode, p046ReadBody(t, submit))
	}
	var submitted commandResponse
	p046DecodeJSON(t, submit, &submitted)
	if submitted.CommandState != string(domain.CommandStateSucceeded) || submitted.ExitCode == nil || *submitted.ExitCode != 0 || !submitted.OutputComplete {
		t.Fatalf("P047 Linux submit response = %+v", submitted)
	}

	events := p046DoJSON(t, client, http.MethodGet, "http://runnerd/internal/v1/commands/p047-linux-command/events?controller_type=direct_mtls&controller_id=tomasz.walczuk&after=0", nil)
	if events.StatusCode != http.StatusOK {
		t.Fatalf("P047 Linux events status = %d, body = %s", events.StatusCode, p046ReadBody(t, events))
	}
	eventBytes, err := io.ReadAll(events.Body)
	events.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	expectedOutput := base64.StdEncoding.EncodeToString([]byte("p047-linux"))
	for _, expected := range []string{"command_queued", "command_started", "stdout", "command_succeeded", expectedOutput} {
		if !strings.Contains(string(eventBytes), expected) {
			t.Fatalf("P047 Linux events missing %q: %s", expected, eventBytes)
		}
	}
}

func mustP047Registry(t *testing.T) *execution.EnvironmentRegistry {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	id, err := domain.NewControllerID("tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, id)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "linux-dev", HostClass: "Ubuntu Linux host", EffectiveAccount: "ubuntu",
		AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := execution.NewEnvironmentRegistry(environment)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func newP046Service(t *testing.T, runtimeAdapter execution.SessionRuntime) (*execution.Service, *store.AuthorityStore) {
	t.Helper()
	fixture := testfixture.New(t)
	db, err := store.Open(context.Background(), filepath.Join(fixture.Path(), "state", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controllerID, err := domain.NewControllerID("tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, controllerID)
	if err != nil {
		t.Fatal(err)
	}
	directController, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, controllerID)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name:               "linux-dev",
		HostClass:          "Ubuntu Linux host",
		EffectiveAccount:   "ubuntu",
		AllowedTargets:     []domain.ExecutionTarget{target},
		AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller, directController},
		ServiceLimits:      domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := execution.NewEnvironmentRegistry(environment)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := execution.NewExecutionService(authority, runtimeAdapter, registry, execution.RealClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service, authority
}

func p046UnixClient(socketPath string) *http.Client {
	transport := &http.Transport{}
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socketPath)
	}
	return &http.Client{Transport: transport}
}

func p046SocketParent(t *testing.T) string {
	t.Helper()
	path, err := os.MkdirTemp(".", "r46-")
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return path
}

func p046DoJSON(t *testing.T, client *http.Client, method, url string, body []byte) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = &p046Reader{bytes: body}
	}
	request, err := http.NewRequestWithContext(context.Background(), method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

type p046Reader struct{ bytes []byte }

func (r *p046Reader) Read(p []byte) (int, error) {
	if len(r.bytes) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.bytes)
	r.bytes = r.bytes[n:]
	return n, nil
}

func p046ReadBody(t *testing.T, response *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func p046DecodeJSON(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}
