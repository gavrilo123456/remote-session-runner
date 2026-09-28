package runnerd

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestP126SignalShutdownClosesBothListenersAndRemovesPrivateSocket(t *testing.T) {
	service, _ := newP046Service(t, &p046FakeRuntime{generation: "p126-test-generation"})
	socketPath := filepath.Join(p046SocketParent(t), "runnerd.sock")
	privateServer, err := NewPrivateServer(PrivateServerOptions{Service: service, SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := privateServer.Listen(); err != nil {
		t.Fatal(err)
	}

	pki := newP106TestPKI(t)
	httpsOptions := writeP106TestFiles(t, t.TempDir(), pki, validP106PrincipalMap)
	httpsServer, err := NewDirectHTTPSServer(httpsOptions)
	if err != nil {
		t.Fatal(err)
	}
	if err := httpsServer.Listen(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveResult := make(chan error, 1)
	go func() { serveResult <- serveUntilSignal(ctx, privateServer, httpsServer) }()

	privateResponse, err := p046UnixClient(socketPath).Get("http://runnerd.invalid/ready-probe")
	if err != nil {
		t.Fatalf("private API probe error = %v", err)
	}
	_ = privateResponse.Body.Close()
	if privateResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("private API probe status = %d, want route-level 404", privateResponse.StatusCode)
	}

	directClient := p106HTTPClient(pki.serverRoots, &pki.clientTLS, tls.VersionTLS13)
	directResponse, err := directClient.Get("https://" + httpsServer.Addr() + "/ready-probe")
	if err != nil {
		t.Fatalf("mTLS HTTPS probe error = %v", err)
	}
	_ = directResponse.Body.Close()
	if directResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("mTLS HTTPS probe status = %d, want route-level 404", directResponse.StatusCode)
	}

	cancel()
	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("serveUntilSignal() error = %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("serveUntilSignal() did not finish after signal cancellation")
	}

	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("private socket after shutdown: Lstat error = %v, want not-exist", err)
	}
	connection, err := net.DialTimeout("tcp", httpsServer.Addr(), 100*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatal("direct HTTPS listener still accepts connections after shutdown")
	}
}
