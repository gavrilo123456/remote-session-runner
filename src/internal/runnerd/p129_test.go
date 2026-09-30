package runnerd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"remote-session-runner/src/internal/opshealth"
)

func TestP129PublicMetricsEndpointRemainsBehindMappedMTLS(t *testing.T) {
	pki := newP106TestPKI(t)
	options := writeP106TestFiles(t, t.TempDir(), pki, validP106PrincipalMap)
	report := opshealth.NewReport("linux_runnerd", time.Unix(1, 0), opshealth.Check{Component: "sqlite_writes", State: opshealth.StateReady, RequiredForReadiness: true})
	metrics := opshealth.Metrics{QueuedCommands: 3, EventGapsTotal: 1}
	report.Metrics = &metrics
	options.Handler = opshealth.Middleware("linux_runnerd", func(context.Context) opshealth.Report { return report }, http.NotFoundHandler())
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
			t.Errorf("serve direct HTTPS: %v", err)
		}
	})

	client := p106HTTPClient(pki.serverRoots, &pki.clientTLS, tls.VersionTLS13)
	response, err := client.Get("https://" + server.Addr() + "/metrics")
	if err != nil {
		t.Fatalf("mapped mTLS /metrics request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("mapped mTLS /metrics status=%d", response.StatusCode)
	}
	var got opshealth.Metrics
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatalf("decode mTLS /metrics: %v", err)
	}
	if !reflect.DeepEqual(got, metrics) {
		t.Fatalf("mTLS /metrics=%+v, want %+v", got, metrics)
	}

	withoutCertificate := p106HTTPClient(pki.serverRoots, nil, tls.VersionTLS13)
	if response, err := withoutCertificate.Get("https://" + server.Addr() + "/metrics"); err == nil {
		_ = response.Body.Close()
		t.Fatalf("/metrics accepted a client without a certificate (status %d)", response.StatusCode)
	}
}
