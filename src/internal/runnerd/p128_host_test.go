//go:build p128opshost

package runnerd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
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
	"remote-session-runner/src/internal/opshealth"
)

func TestP128PublicHealthRequiresMandatoryMTLS(t *testing.T) {
	if os.Getenv("RSR_P128_OPS_HOST_GATE") != "1" {
		t.Skip("set RSR_P128_OPS_HOST_GATE=1 to run the public Ubuntu health gate")
	}
	if runtime.GOOS != "darwin" {
		t.Fatalf("P128 public-health gate must run from the selected Mac, got %s", runtime.GOOS)
	}
	current, err := user.Current()
	if err != nil || current.Username != config.MacAccount {
		t.Fatalf("P128 public-health account=%v err=%v, want %s", current, err, config.MacAccount)
	}
	caPath := p128RequiredCredentialPath(t, "RUNNER_P128_SERVER_CA")
	certPath := p128RequiredCredentialPath(t, "RUNNER_P128_CLIENT_CERT")
	keyPath := p128RequiredCredentialPath(t, "RUNNER_P128_CLIENT_KEY")
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load owner-only P128 mTLS identity: %v", err)
	}
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("read P128 server trust anchor: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("P128 server trust anchor contains no CA certificates")
	}
	endpoint := config.PublicEndpoint
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{certificate}}}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second}
	defer transport.CloseIdleConnections()
	for _, path := range []string{"/health/live", "/health/ready"} {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, endpoint+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("GET %s with mandatory mTLS: %v", path, err)
		}
		var report opshealth.Report
		decodeErr := json.NewDecoder(response.Body).Decode(&report)
		closeErr := response.Body.Close()
		if decodeErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status=%d decode=%v close=%v", path, response.StatusCode, decodeErr, closeErr)
		}
		if path == "/health/live" {
			if report.Liveness != "live" {
				t.Fatalf("public liveness report=%+v", report)
			}
			continue
		}
		if report.Component != "linux_runnerd" || report.Readiness != opshealth.StateReady || len(report.Checks) != 3 {
			t.Fatalf("public readiness report=%+v", report)
		}
		for _, check := range report.Checks {
			if check.State != opshealth.StateReady {
				t.Fatalf("public readiness dependency=%+v", check)
			}
		}
	}

	unauthenticated := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}},
		Timeout:   20 * time.Second,
	}
	defer unauthenticated.CloseIdleConnections()
	request, err := http.NewRequest(http.MethodGet, endpoint+"/health/ready", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := unauthenticated.Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("public health endpoint accepted a client without a certificate")
	}
	if strings.Contains(err.Error(), keyPath) || strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Fatal("unauthenticated health error exposed credential information")
	}
	t.Logf("machine=Mac account=%s uid=%s public=%s health endpoints passed TLS 1.3 mTLS; no-client-certificate request was rejected", current.Username, current.Uid, endpoint)
}

func p128RequiredCredentialPath(t *testing.T, name string) string {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(name))
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatalf("%s must be an absolute owner-only credential path", name)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect %s: %v", name, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint32(stat.Uid) != uint32(os.Geteuid()) || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("%s must be a regular owner-only file", name)
	}
	return path
}
