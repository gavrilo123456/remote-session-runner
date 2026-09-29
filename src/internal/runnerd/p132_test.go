package runnerd

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/lifecycle"
)

func TestP132AdmissionAndDispatchGatesStopNewWork(t *testing.T) {
	requestGate := lifecycle.NewGate()
	workStarted := make(chan struct{})
	workRelease := make(chan struct{})
	if !launchRunnerWork(requestGate, func() {
		close(workStarted)
		<-workRelease
	}) {
		t.Fatal("launchRunnerWork() rejected work while the gate was open")
	}
	<-workStarted

	streamRelease, admitted := admitRunnerRequest(requestGate, "/v1/commands/cmd-p132/events", http.MethodGet, nil)
	if !admitted {
		t.Fatal("admitRunnerRequest() rejected an event follower while open")
	}
	streamRelease()
	requestGate.Stop()
	if _, admitted := admitRunnerRequest(requestGate, "/v1/sessions", http.MethodPost, nil); admitted {
		t.Fatal("admitRunnerRequest() admitted a mutation after shutdown began")
	}

	dispatchGate := lifecycle.NewGate()
	dispatchGate.Stop()
	started := false
	if launchRunnerWork(dispatchGate, func() { started = true }) {
		t.Fatal("launchRunnerWork() admitted new dispatch after shutdown began")
	}
	if started {
		t.Fatal("work ran after dispatch admission was closed")
	}

	close(workRelease)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := requestGate.Wait(ctx); err != nil {
		t.Fatalf("request gate did not drain admitted work: %v", err)
	}
}

func p132PrivateSocketPath(t *testing.T) string {
	t.Helper()
	parent, err := os.MkdirTemp("/tmp", "r132-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(parent, "runnerd.sock")
}

func TestP132ShutdownClosesSessionsThroughExecutionService(t *testing.T) {
	service, authority := newP046Service(t, &p046FakeRuntime{generation: "p132-shutdown-generation"})
	socketPath := p132PrivateSocketPath(t)
	server, err := NewPrivateServer(PrivateServerOptions{Service: service, SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve() }()
	t.Cleanup(func() {
		_ = server.Close(context.Background())
		<-serveDone
	})

	const sessionID = "p132-shutdown-session"
	body := []byte(`{"session_id":"p132-shutdown-session","idempotency_key":"p132-shutdown-create","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"queued_mac","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"}}`)
	response := p046DoJSON(t, p046UnixClient(socketPath), http.MethodPost, "http://runnerd/internal/v1/sessions", body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("create session status=%d body=%s", response.StatusCode, p046ReadBody(t, response))
	}
	_ = response.Body.Close()

	hooks := &linuxShutdownHooks{service: service, authority: authority}
	if err := hooks.closeActiveSessions(context.Background()); err != nil {
		t.Fatalf("closeActiveSessions() error = %v", err)
	}
	session, err := authority.GetSession(context.Background(), domain.SessionID(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if session.State != domain.SessionStateClosed {
		t.Fatalf("shutdown session state=%q, want closed", session.State)
	}
	records, err := authority.ListAuditRecords(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var lifecycleCloseFound bool
	for _, record := range records {
		if record.SessionID == domain.SessionID(sessionID) && record.Action == audit.ActionClose && record.Outcome == audit.OutcomeAllowed && record.Ingress == audit.IngressServiceLifecycle {
			lifecycleCloseFound = true
		}
	}
	if !lifecycleCloseFound {
		t.Fatalf("shutdown did not durably audit its normal close path: %+v", records)
	}
}

func TestP132CoordinatorDrainsBothIngressesBeforeClosingStreams(t *testing.T) {
	service, authority := newP046Service(t, &p046FakeRuntime{generation: "p132-coordinator-generation"})
	requestGate := lifecycle.NewGate()
	dispatchGate := lifecycle.NewGate()
	socketPath := p132PrivateSocketPath(t)
	privateServer, err := NewPrivateServer(PrivateServerOptions{
		Service: service, SocketPath: socketPath, RequestGate: requestGate, DispatchGate: dispatchGate,
	})
	if err != nil {
		t.Fatal(err)
	}
	pki := newP106TestPKI(t)
	httpsOptions := writeP106TestFiles(t, t.TempDir(), pki, validP106PrincipalMap)
	httpsOptions.Handler, err = newDirectHTTPSAPIHandler(service, requestGate, dispatchGate)
	if err != nil {
		t.Fatal(err)
	}
	httpsServer, err := NewDirectHTTPSServer(httpsOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err := privateServer.Listen(); err != nil {
		t.Fatal(err)
	}
	if err := httpsServer.Listen(); err != nil {
		_ = privateServer.Close(context.Background())
		t.Fatal(err)
	}
	hooks := &linuxShutdownHooks{
		private: privateServer, https: httpsServer, service: service, authority: authority,
		requestGate: requestGate, dispatchGate: dispatchGate,
	}
	coordinator, err := lifecycle.NewCoordinator(hooks, lifecycle.RealClock{}, lifecycle.Config{
		DrainTimeout: time.Second, CleanupTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- serveUntilCoordinator(ctx, privateServer, httpsServer, coordinator) }()
	t.Cleanup(func() {
		cancel()
		_ = privateServer.Close(context.Background())
		_ = httpsServer.Close(context.Background())
		select {
		case <-serveDone:
		case <-time.After(time.Second):
		}
	})

	const sessionID = "p132-coordinator-session"
	body := []byte(`{"session_id":"p132-coordinator-session","idempotency_key":"p132-coordinator-create","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"queued_mac","controller_id":"tomasz.walczuk"},"source":{"mode":"empty"}}`)
	response := p046DoJSON(t, p046UnixClient(socketPath), http.MethodPost, "http://runnerd/internal/v1/sessions", body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("create session status=%d body=%s", response.StatusCode, p046ReadBody(t, response))
	}
	_ = response.Body.Close()
	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("serveUntilCoordinator() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runnerd coordinator did not finish within the bounded test deadline")
	}

	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("runnerd socket after coordinated shutdown: %v, want not-exist", err)
	}
	session, err := authority.GetSession(context.Background(), domain.SessionID(sessionID))
	if err != nil || session.State != domain.SessionStateClosed {
		t.Fatalf("session after coordinator shutdown=%q err=%v, want closed", session.State, err)
	}
	if release, err := requestGate.Enter(); err == nil {
		release()
		t.Fatal("request gate admitted new work after coordinated shutdown")
	}
	if release, err := dispatchGate.Enter(); err == nil {
		release()
		t.Fatal("dispatch gate admitted new work after coordinated shutdown")
	}
	connection, err := net.DialTimeout("tcp", httpsServer.Addr(), 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatal("direct HTTPS listener still accepts connections after coordinated shutdown")
	}
}

func TestP132CloseStreamsRefusesReplacedPrivateSocket(t *testing.T) {
	service, _ := newP046Service(t, &p046FakeRuntime{generation: "p132-socket-generation"})
	socketPath := p132PrivateSocketPath(t)
	server, err := NewPrivateServer(PrivateServerOptions{Service: service, SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socketPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socketPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := server.CloseStreams(); err == nil {
		t.Fatal("CloseStreams() did not report a replaced socket path")
	}
	contents, err := os.ReadFile(socketPath)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("replaced private socket path contents=%q err=%v, want preserved file", contents, err)
	}
}
