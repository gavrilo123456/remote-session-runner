package runnerd

import (
	"context"
	"crypto/tls"
	"net/http"
	"testing"
	"time"
)

// The online retained-capacity repair is deliberately a local owner command.
// It must never become a remotely reachable API route merely because its
// runnerd subcommand is registered in Run.
func TestBUG008RecoverRetainedCapacityIsNotAPublicDirectHTTPSRoute(t *testing.T) {
	service, _ := newP046Service(t, &p046FakeRuntime{generation: "bug008-boundary-public"})
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	pki := newP106TestPKI(t)
	options := writeP106TestFiles(t, t.TempDir(), pki, validP106PrincipalMap)
	options.Handler = handler
	server, err := NewDirectHTTPSServer(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close direct HTTPS server: %v", err)
		}
		if err := <-serveResult; err != nil {
			t.Errorf("serve direct HTTPS server: %v", err)
		}
	})

	request, err := http.NewRequest(http.MethodPost, "https://"+server.Addr()+"/v1/recover-retained-capacity", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := p106HTTPClient(pki.serverRoots, &pki.clientTLS, tls.VersionTLS13).Do(request)
	if err != nil {
		t.Fatalf("mapped mTLS request: %v", err)
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("public recover-retained-capacity status=%d body=%s, want 404", response.StatusCode, p107HTTPBody(t, response))
	}
	_ = response.Body.Close()
}

// The private Unix service exists for the frozen resource protocol and the
// SSH bridge. The maintenance command must not become a socket route through
// either the internal path or its v1 compatibility alias.
func TestBUG008RecoverRetainedCapacityIsNotAPrivateSocketRoute(t *testing.T) {
	service, _ := newP046Service(t, &p046FakeRuntime{generation: "bug008-boundary-private"})
	server, serveResult := p048StartServer(t, service)
	t.Cleanup(func() { p048CloseServer(t, server, serveResult) })
	client := p046UnixClient(server.SocketPath())

	for _, path := range []string{
		"/internal/v1/recover-retained-capacity",
		"/v1/recover-retained-capacity",
	} {
		t.Run(path, func(t *testing.T) {
			response := p046DoJSON(t, client, http.MethodPost, "http://runnerd"+path, nil)
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("private recover-retained-capacity status=%d body=%s, want 404", response.StatusCode, p046ReadBody(t, response))
			}
			_ = response.Body.Close()
		})
	}
}
