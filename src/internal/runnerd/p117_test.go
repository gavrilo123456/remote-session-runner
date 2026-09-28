//go:build p117twohost

package runnerd

import (
	"bufio"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const p117PublicEndpoint = "https://129.151.232.40:8443"

func TestP117PublicEndpointPNET02(t *testing.T) {
	roots := p117LoadServerRoots(t)
	primaryCertificate := p117LoadClientCertificate(t, "RUNNER_P117_CLIENT_CERT", "RUNNER_P117_CLIENT_KEY")
	otherCertificate := p117LoadClientCertificate(t, "RUNNER_P117_OTHER_CLIENT_CERT", "RUNNER_P117_OTHER_CLIENT_KEY")
	expiredCertificate := p117LoadClientCertificate(t, "RUNNER_P117_EXPIRED_CLIENT_CERT", "RUNNER_P117_EXPIRED_CLIENT_KEY")
	untrustedCertificate := p117LoadClientCertificate(t, "RUNNER_P117_UNTRUSTED_CLIENT_CERT", "RUNNER_P117_UNTRUSTED_CLIENT_KEY")
	unmappedCertificate := p117LoadClientCertificate(t, "RUNNER_P117_UNMAPPED_CLIENT_CERT", "RUNNER_P117_UNMAPPED_CLIENT_KEY")
	p117ValidateClientFixtureRoles(t, roots, primaryCertificate, otherCertificate, expiredCertificate, untrustedCertificate, unmappedCertificate)

	t.Run("server IP SAN and mapped mTLS application success", func(t *testing.T) {
		client := p117HTTPClient(roots, &primaryCertificate, tls.VersionTLS13, tls.VersionTLS13, "129.151.232.40")
		t.Cleanup(client.CloseIdleConnections)
		p117RequirePublicServerIdentity(t, client)
		p117ExerciseAuthorizedAPI(t, client, roots, &otherCertificate)
	})

	t.Run("wrong server trust root rejected", func(t *testing.T) {
		client := p117HTTPClient(x509.NewCertPool(), &primaryCertificate, tls.VersionTLS13, tls.VersionTLS13, "129.151.232.40")
		t.Cleanup(client.CloseIdleConnections)
		p117RequireTLSRejection(t, client, false)
	})
	t.Run("DNS name without a DNS SAN rejected", func(t *testing.T) {
		client := p117HTTPClient(roots, &primaryCertificate, tls.VersionTLS13, tls.VersionTLS13, "runner-linux-dev")
		t.Cleanup(client.CloseIdleConnections)
		p117RequireTLSRejection(t, client, false)
	})
	t.Run("wrong IP SAN rejected", func(t *testing.T) {
		client := p117HTTPClient(roots, &primaryCertificate, tls.VersionTLS13, tls.VersionTLS13, "129.151.232.41")
		t.Cleanup(client.CloseIdleConnections)
		p117RequireTLSRejection(t, client, false)
	})
	t.Run("TLS below 1.3 rejected", func(t *testing.T) {
		client := p117HTTPClient(roots, &primaryCertificate, tls.VersionTLS12, tls.VersionTLS12, "129.151.232.40")
		t.Cleanup(client.CloseIdleConnections)
		p117RequireTLSRejection(t, client, false)
	})
	t.Run("missing client certificate and bearer-only request rejected", func(t *testing.T) {
		client := p117HTTPClient(roots, nil, tls.VersionTLS13, tls.VersionTLS13, "129.151.232.40")
		t.Cleanup(client.CloseIdleConnections)
		p117RequireTLSRejection(t, client, true)
	})
	t.Run("plaintext HTTP and bearer fallback rejected", func(t *testing.T) {
		client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
		t.Cleanup(client.CloseIdleConnections)
		request, err := http.NewRequest(http.MethodGet, "http://129.151.232.40:8443/v1/sessions/p117-plaintext-probe", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer p117-no-fallback")
		response, err := client.Do(request)
		if err != nil {
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusBadRequest || strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("plaintext request reached an application or unexpected handler: status=%d content-type=%q", response.StatusCode, response.Header.Get("Content-Type"))
		}
	})
	t.Run("untrusted client certificate rejected", func(t *testing.T) {
		client := p117HTTPClient(roots, &untrustedCertificate, tls.VersionTLS13, tls.VersionTLS13, "129.151.232.40")
		t.Cleanup(client.CloseIdleConnections)
		p117RequireTLSRejection(t, client, false)
	})
	t.Run("expired client certificate rejected", func(t *testing.T) {
		client := p117HTTPClient(roots, &expiredCertificate, tls.VersionTLS13, tls.VersionTLS13, "129.151.232.40")
		t.Cleanup(client.CloseIdleConnections)
		p117RequireTLSRejection(t, client, false)
	})
	t.Run("valid but unmapped client identity rejected", func(t *testing.T) {
		client := p117HTTPClient(roots, &unmappedCertificate, tls.VersionTLS13, tls.VersionTLS13, "129.151.232.40")
		t.Cleanup(client.CloseIdleConnections)
		p117RequireTLSRejection(t, client, false)
	})

	t.Run("forged controller body field is rejected", func(t *testing.T) {
		client := p117HTTPClient(roots, &otherCertificate, tls.VersionTLS13, tls.VersionTLS13, "129.151.232.40")
		t.Cleanup(client.CloseIdleConnections)
		body := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"controller":{"controller_type":"direct_mtls","controller_id":"tomasz.walczuk"}}`)
		status, responseBody := p117Do(t, client, http.MethodPost, p117PublicEndpoint+"/v1/sessions", body, p117Key(t), "")
		p117RequireAPIError(t, status, responseBody, http.StatusBadRequest, "invalid_request")
	})
}

func p117ExerciseAuthorizedAPI(t *testing.T, owner *http.Client, roots *x509.CertPool, otherCertificate *tls.Certificate) {
	t.Helper()
	status, body := p117Do(t, owner, http.MethodPost, p117PublicEndpoint+"/v1/sessions", []byte(p107CreateBody), p117Key(t), "")
	p117RequireStatus(t, status, body, http.StatusAccepted)
	var acceptedSession directSessionAcceptance
	if err := json.Unmarshal(body, &acceptedSession); err != nil || acceptedSession.SessionID == "" {
		t.Fatalf("decode public session acceptance: %v", err)
	}
	sessionID := acceptedSession.SessionID
	t.Cleanup(func() { p117CleanupSession(t, owner, sessionID) })
	p117WaitForSessionState(t, owner, sessionID, "ready")

	status, body = p117Do(t, owner, http.MethodGet, p117PublicEndpoint+"/v1/sessions/"+sessionID, nil, "", "")
	p117RequireStatus(t, status, body, http.StatusOK)
	var sessionRead directSessionReadResponse
	if err := json.Unmarshal(body, &sessionRead); err != nil {
		t.Fatalf("decode public session read: %v", err)
	}
	if sessionRead.Resource.Controller.Type != "direct_mtls" || sessionRead.Resource.Controller.ID != "tomasz.walczuk" || sessionRead.Resource.ExecutionTarget.Kind != "remote" {
		t.Fatalf("session identity was not derived from the mapped certificate: %+v", sessionRead.Resource)
	}

	const output = "p117-public-mtls-ok\n"
	submitBody := []byte(`{"script":"printf 'p117-public-mtls-ok\\n'"}`)
	status, body = p117Do(t, owner, http.MethodPost, p117PublicEndpoint+"/v1/sessions/"+sessionID+"/commands", submitBody, p117Key(t), "")
	p117RequireStatus(t, status, body, http.StatusAccepted)
	var acceptedCommand directCommandAcceptance
	if err := json.Unmarshal(body, &acceptedCommand); err != nil || acceptedCommand.CommandID == "" {
		t.Fatalf("decode public command acceptance: %v", err)
	}
	commandID := acceptedCommand.CommandID
	p117WaitForCommandState(t, owner, commandID, "succeeded")

	status, body = p117Do(t, owner, http.MethodGet, p117PublicEndpoint+"/v1/commands/"+commandID, nil, "", "")
	p117RequireStatus(t, status, body, http.StatusOK)
	var commandRead directCommandReadResponse
	if err := json.Unmarshal(body, &commandRead); err != nil {
		t.Fatalf("decode public command read: %v", err)
	}
	if commandRead.Resource.CommandState != "succeeded" || commandRead.Resource.Controller.ID != "tomasz.walczuk" {
		t.Fatalf("command result/controller mismatch: %+v", commandRead.Resource)
	}

	status, body = p117Do(t, owner, http.MethodGet, p117PublicEndpoint+"/v1/commands/"+commandID+"/events?after=0", nil, "", "")
	p117RequireStatus(t, status, body, http.StatusOK)
	if !strings.HasPrefix(p117ResponseContentType(t, owner, p117PublicEndpoint+"/v1/commands/"+commandID+"/events?after=0"), "application/x-ndjson") {
		t.Fatal("public event endpoint did not return NDJSON")
	}
	events := p117DecodeEvents(t, body)
	if len(events) == 0 {
		t.Fatal("public command event stream was empty")
	}
	var outputSeen bool
	for index, event := range events {
		if event.CommandID != commandID || event.Sequence != int64(index+1) {
			t.Fatalf("event[%d] identity/sequence = %+v", index, event)
		}
		if event.Type == "stdout" {
			decoded, err := base64.StdEncoding.DecodeString(event.DataBase64)
			if err != nil || string(decoded) != output || event.ByteCount != int64(len(output)) {
				t.Fatalf("public stdout event payload=%q byte_count=%d err=%v", decoded, event.ByteCount, err)
			}
			outputSeen = true
		}
	}
	if !outputSeen {
		t.Fatal("public event stream omitted the command's stdout")
	}

	other := p117HTTPClient(roots, otherCertificate, tls.VersionTLS13, tls.VersionTLS13, "129.151.232.40")
	t.Cleanup(other.CloseIdleConnections)
	p117RequireControllerDenial(t, other, http.MethodGet, "/v1/sessions/"+sessionID, nil, "")
	p117RequireControllerDenial(t, other, http.MethodGet, "/v1/commands/"+commandID, nil, "")
	p117RequireControllerDenial(t, other, http.MethodGet, "/v1/commands/"+commandID+"/events?after=0", nil, "")
	p117RequireControllerDenial(t, other, http.MethodPost, "/v1/sessions/"+sessionID+"/commands", []byte(`{"script":"printf denied"}`), p117Key(t))
	p117RequireControllerDenial(t, other, http.MethodPost, "/v1/commands/"+commandID+"/cancel", nil, p117Key(t))
	p117RequireControllerDenial(t, other, http.MethodDelete, "/v1/sessions/"+sessionID, nil, p117Key(t))

	status, body = p117Do(t, owner, http.MethodGet, p117PublicEndpoint+"/v1/sessions/"+sessionID, nil, "", "")
	p117RequireStatus(t, status, body, http.StatusOK)
	if err := json.Unmarshal(body, &sessionRead); err != nil || sessionRead.Resource.SessionState != "ready" {
		t.Fatalf("cross-controller attempts changed owner session: state=%q decode=%v", sessionRead.Resource.SessionState, err)
	}
	status, body = p117Do(t, owner, http.MethodGet, p117PublicEndpoint+"/v1/commands/"+commandID, nil, "", "")
	p117RequireStatus(t, status, body, http.StatusOK)
	if err := json.Unmarshal(body, &commandRead); err != nil || commandRead.Resource.CommandState != "succeeded" {
		t.Fatalf("cross-controller attempts changed owner command: state=%q decode=%v", commandRead.Resource.CommandState, err)
	}
}

func p117LoadServerRoots(t *testing.T) *x509.CertPool {
	t.Helper()
	path := p117RequiredEnv(t, "RUNNER_P117_SERVER_CA")
	contents, err := p117ReadFixture(t, path, false)
	if err != nil {
		t.Fatalf("RUNNER_P117_SERVER_CA fixture is unavailable: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(contents) {
		t.Fatal("RUNNER_P117_SERVER_CA contains no trusted certificates")
	}
	return roots
}

func p117LoadClientCertificate(t *testing.T, certVariable, keyVariable string) tls.Certificate {
	t.Helper()
	certPath := p117RequiredEnv(t, certVariable)
	keyPath := p117RequiredEnv(t, keyVariable)
	if _, err := os.Stat(certPath); err != nil {
		t.Fatalf("%s fixture is unavailable", certVariable)
	}
	if _, err := p117ReadFixture(t, keyPath, true); err != nil {
		t.Fatalf("%s fixture is unavailable or not owner-only", keyVariable)
	}
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load %s/%s TLS fixture: %v", certVariable, keyVariable, err)
	}
	return certificate
}

func p117ValidateClientFixtureRoles(t *testing.T, roots *x509.CertPool, primary, other, expired, untrusted, unmapped tls.Certificate) {
	t.Helper()
	now := time.Now()
	assertCurrentTrustedClient := func(label string, certificate tls.Certificate, expectedURI string) *x509.Certificate {
		t.Helper()
		leaf := p117LeafCertificate(t, label, certificate)
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: now}); err != nil {
			t.Fatalf("%s fixture is not currently trusted for client authentication: %v", label, err)
		}
		if len(leaf.URIs) != 1 || leaf.URIs[0].String() != expectedURI {
			t.Fatalf("%s fixture URI SANs = %v, want %q", label, leaf.URIs, expectedURI)
		}
		return leaf
	}
	assertCurrentTrustedClient("primary mapped client", primary, "urn:remote-session-runner:controller:runner-tomasz-direct")
	assertCurrentTrustedClient("secondary mapped client", other, "urn:remote-session-runner:controller:p117-cross-controller")
	assertCurrentTrustedClient("unmapped client", unmapped, "urn:remote-session-runner:controller:p117-unmapped")

	expiredLeaf := p117LeafCertificate(t, "expired client", expired)
	if !expiredLeaf.NotAfter.Before(now) {
		t.Fatalf("expired client fixture is still valid through %s", expiredLeaf.NotAfter)
	}
	if _, err := expiredLeaf.Verify(x509.VerifyOptions{
		Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: expiredLeaf.NotBefore.Add(time.Minute),
	}); err != nil {
		t.Fatalf("expired client fixture does not have a trusted client-auth chain apart from its expiry: %v", err)
	}

	untrustedLeaf := p117LeafCertificate(t, "untrusted client", untrusted)
	if now.Before(untrustedLeaf.NotBefore) || now.After(untrustedLeaf.NotAfter) {
		t.Fatalf("untrusted client fixture is not currently within its validity period")
	}
	_, err := untrustedLeaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, CurrentTime: now})
	var unknownAuthority x509.UnknownAuthorityError
	if !errors.As(err, &unknownAuthority) {
		t.Fatalf("untrusted client fixture verification error = %v, want unknown authority", err)
	}
}

func p117LeafCertificate(t *testing.T, label string, certificate tls.Certificate) *x509.Certificate {
	t.Helper()
	if len(certificate.Certificate) == 0 {
		t.Fatalf("%s fixture contains no leaf certificate", label)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatalf("parse %s leaf certificate: %v", label, err)
	}
	return leaf
}

func p117RequiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s must name a P117 fixture file", name)
	}
	return value
}

func p117ReadFixture(t *testing.T, path string, private bool) ([]byte, error) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || filepath.Clean(path) != path || info.Size() > 1<<20 {
		return nil, errors.New("fixture must be a small regular file")
	}
	if private {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o400 == 0 {
			return nil, errors.New("private fixture must be owned by the current user and mode 0600 or stricter")
		}
	}
	return os.ReadFile(path)
}

func p117HTTPClient(roots *x509.CertPool, certificate *tls.Certificate, minVersion, maxVersion uint16, serverName string) *http.Client {
	tlsConfig := &tls.Config{RootCAs: roots, MinVersion: minVersion, MaxVersion: maxVersion, ServerName: serverName}
	if certificate != nil {
		tlsConfig.Certificates = []tls.Certificate{*certificate}
	}
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy:             nil,
			TLSClientConfig:   tlsConfig,
			ForceAttemptHTTP2: false,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func p117RequirePublicServerIdentity(t *testing.T, client *http.Client) {
	t.Helper()
	response, err := client.Get(p117PublicEndpoint + "/v1/sessions/session-p117-server-identity-probe")
	if err != nil {
		t.Fatalf("valid public TLS request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("valid client did not reach the Runner API: status=%d body=%s", response.StatusCode, body)
	}
	if response.TLS == nil || response.TLS.Version != tls.VersionTLS13 || len(response.TLS.VerifiedChains) == 0 || len(response.TLS.PeerCertificates) == 0 {
		t.Fatalf("public TLS state did not prove TLS 1.3 and server-chain verification: %+v", response.TLS)
	}
	if err := response.TLS.PeerCertificates[0].VerifyHostname("129.151.232.40"); err != nil {
		t.Fatalf("public server certificate does not verify the configured IP SAN: %v", err)
	}
}

func p117RequireTLSRejection(t *testing.T, client *http.Client, bearerOnly bool) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, p117PublicEndpoint+"/v1/sessions/p117-tls-negative-probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearerOnly {
		request.Header.Set("Authorization", "Bearer p117-no-fallback")
	}
	response, err := client.Do(request)
	if err == nil {
		defer response.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("invalid TLS peer reached HTTP: status=%d body=%s", response.StatusCode, body)
	}
	var verificationFailure *tls.CertificateVerificationError
	var protocolAlert tls.AlertError
	remoteTLSAlert := strings.Contains(err.Error(), "remote error: tls:")
	if !errors.As(err, &verificationFailure) && !errors.As(err, &protocolAlert) && !remoteTLSAlert {
		t.Fatalf("negative TLS case failed without a TLS verification error or alert: %v", err)
	}
}

func p117ResponseContentType(t *testing.T, client *http.Client, path string) string {
	t.Helper()
	response, err := client.Get(path)
	if err != nil {
		t.Fatalf("read public event content type: %v", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.Header.Get("Content-Type")
}

func p117Do(t *testing.T, client *http.Client, method, url string, requestBody []byte, idempotencyKey, authorization string) (int, []byte) {
	t.Helper()
	var body io.Reader
	if requestBody != nil {
		body = strings.NewReader(string(requestBody))
	}
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("P117 %s public request failed: %v", method, err)
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatalf("read P117 public response: %v", err)
	}
	return response.StatusCode, contents
}

func p117RequireStatus(t *testing.T, status int, body []byte, want int) {
	t.Helper()
	if status != want {
		t.Fatalf("P117 HTTP status=%d, want %d; body=%s", status, want, body)
	}
}

func p117RequireAPIError(t *testing.T, status int, body []byte, wantStatus int, wantCode string) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("P117 denial status=%d, want %d; body=%s", status, wantStatus, body)
	}
	var apiError directAPIError
	if err := json.Unmarshal(body, &apiError); err != nil || apiError.Code != wantCode {
		t.Fatalf("P117 denial error=%+v decode=%v; want code %q", apiError, err, wantCode)
	}
}

func p117RequireControllerDenial(t *testing.T, client *http.Client, method, path string, body []byte, key string) {
	t.Helper()
	status, responseBody := p117Do(t, client, method, p117PublicEndpoint+path, body, key, "")
	p117RequireAPIError(t, status, responseBody, http.StatusForbidden, "controller_mismatch")
}

func p117DecodeEvents(t *testing.T, contents []byte) []directCommandEvent {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	scanner.Buffer(make([]byte, 1024), 64*1024)
	var events []directCommandEvent
	for scanner.Scan() {
		var event directCommandEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode public event frame: %v", err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func p117WaitForSessionState(t *testing.T, client *http.Client, sessionID, expected string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, body := p117Do(t, client, http.MethodGet, p117PublicEndpoint+"/v1/sessions/"+sessionID, nil, "", "")
		if status != http.StatusOK {
			t.Fatalf("P117 session poll status=%d body=%s", status, body)
		}
		var result directSessionReadResponse
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("decode P117 session poll: %v", err)
		}
		if result.Resource.SessionState == expected {
			return
		}
		if result.Resource.SessionState == "failed" || result.Resource.SessionState == "lost" || result.Resource.SessionState == "closed" {
			t.Fatalf("P117 session ended in %q before reaching %q", result.Resource.SessionState, expected)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("P117 session %q did not reach %q before timeout", sessionID, expected)
}

func p117WaitForCommandState(t *testing.T, client *http.Client, commandID, expected string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		status, body := p117Do(t, client, http.MethodGet, p117PublicEndpoint+"/v1/commands/"+commandID, nil, "", "")
		if status != http.StatusOK {
			t.Fatalf("P117 command poll status=%d body=%s", status, body)
		}
		var result directCommandReadResponse
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("decode P117 command poll: %v", err)
		}
		if result.Resource.CommandState == expected {
			return
		}
		if result.Resource.CommandState == "failed" || result.Resource.CommandState == "lost" || result.Resource.CommandState == "cancelled" {
			t.Fatalf("P117 command ended in %q before reaching %q", result.Resource.CommandState, expected)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("P117 command %q did not reach %q before timeout", commandID, expected)
}

func p117CleanupSession(t *testing.T, client *http.Client, sessionID string) {
	t.Helper()
	status, body := p117Do(t, client, http.MethodDelete, p117PublicEndpoint+"/v1/sessions/"+sessionID, nil, p117Key(t), "")
	if status != http.StatusAccepted && status != http.StatusOK {
		t.Errorf("P117 cleanup close status=%d body=%s", status, body)
		return
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		status, body = p117Do(t, client, http.MethodGet, p117PublicEndpoint+"/v1/sessions/"+sessionID, nil, "", "")
		if status != http.StatusOK {
			t.Errorf("P117 cleanup session read status=%d body=%s", status, body)
			return
		}
		var result directSessionReadResponse
		if err := json.Unmarshal(body, &result); err == nil && result.Resource.SessionState == "closed" {
			return
		}
		if err := json.Unmarshal(body, &result); err == nil && result.Resource.SessionState == "lost" {
			t.Errorf("P117 session %q teardown is unconfirmed (state=lost)", sessionID)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("P117 session %q did not confirm teardown before cleanup timeout", sessionID)
}

func p117Key(t *testing.T) string {
	t.Helper()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatalf("generate P117 idempotency key: %v", err)
	}
	return fmt.Sprintf("p117-%x", random[:])
}
