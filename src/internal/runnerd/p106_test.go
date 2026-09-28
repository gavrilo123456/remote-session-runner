package runnerd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const p106MappedURI = "urn:remote-session-runner:controller:runner-tomasz-direct"
const p106OtherMappedURI = "urn:remote-session-runner:controller:runner-other-direct"

type p106TestPKI struct {
	caPEM          []byte
	serverCert     []byte
	serverKey      []byte
	clientTLS      tls.Certificate
	otherClientTLS tls.Certificate
	unknownTLS     tls.Certificate
	unmappedTLS    tls.Certificate
	serverRoots    *x509.CertPool
}

func TestP106DirectHTTPSRequiresTLS13AndMappedClientCertificate(t *testing.T) {
	root := t.TempDir()
	pki := newP106TestPKI(t)
	options := writeP106TestFiles(t, root, pki, validP106PrincipalMap)
	var handlerCalls atomic.Int32
	options.Handler = http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		principal, ok := DirectPrincipalFromContext(request.Context())
		if !ok {
			http.Error(response, "principal missing", http.StatusInternalServerError)
			return
		}
		handlerCalls.Add(1)
		_, _ = fmt.Fprintf(response, "%s:%s", principal.Controller.Type(), principal.Controller.ID())
	})
	server, err := NewDirectHTTPSServer(options)
	if err != nil {
		t.Fatalf("NewDirectHTTPSServer() error = %v", err)
	}
	if err := server.Listen(); err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
		if err := <-serveResult; err != nil {
			t.Errorf("Serve() error = %v", err)
		}
	})

	client := p106HTTPClient(pki.serverRoots, &pki.clientTLS, tls.VersionTLS13)
	response, err := client.Get("https://" + server.Addr() + "/no-resource-route-yet")
	if err != nil {
		t.Fatalf("valid mapped client request error = %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatalf("read valid client body: %v", readErr)
	}
	if response.StatusCode != http.StatusOK || string(body) != "direct_mtls:tomasz.walczuk" {
		t.Fatalf("valid mapped client response = %d %q", response.StatusCode, body)
	}

	for _, test := range []struct {
		name       string
		cert       *tls.Certificate
		maxVersion uint16
		bearerOnly bool
	}{
		{name: "missing client certificate", bearerOnly: true},
		{name: "untrusted client certificate", cert: &pki.unknownTLS},
		{name: "unmapped client identity", cert: &pki.unmappedTLS},
		{name: "TLS 1.2 only", cert: &pki.clientTLS, maxVersion: tls.VersionTLS12},
	} {
		t.Run(test.name, func(t *testing.T) {
			maxVersion := test.maxVersion
			if maxVersion == 0 {
				maxVersion = tls.VersionTLS13
			}
			invalidClient := p106HTTPClient(pki.serverRoots, test.cert, maxVersion)
			request, err := http.NewRequest(http.MethodGet, "https://"+server.Addr()+"/must-not-run", nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.bearerOnly {
				request.Header.Set("Authorization", "Bearer p106-test-only")
			}
			resp, err := invalidClient.Do(request)
			if err == nil {
				_ = resp.Body.Close()
				t.Fatalf("invalid client unexpectedly reached HTTP with status %d", resp.StatusCode)
			}
		})
	}
	plainHTTP := &http.Client{Timeout: 2 * time.Second}
	plainResponse, plainErr := plainHTTP.Get("http://" + server.Addr() + "/must-not-run")
	if plainErr == nil && plainResponse.StatusCode != http.StatusBadRequest {
		_ = plainResponse.Body.Close()
		t.Fatalf("plaintext HTTP unexpectedly reached the application with status %d", plainResponse.StatusCode)
	}
	if plainResponse != nil {
		_ = plainResponse.Body.Close()
	}
	if got := handlerCalls.Load(); got != 1 {
		t.Fatalf("HTTP handler calls = %d, want only the one mapped TLS 1.3 request", got)
	}
}

func TestP106DirectHTTPSRefusesMissingOrInvalidPrincipalMap(t *testing.T) {
	pki := newP106TestPKI(t)
	for _, test := range []struct {
		name string
		body string
		mode os.FileMode
	}{
		{name: "empty", body: "version: 1\nprincipals: []\n", mode: 0o600},
		{name: "wrong version", body: "version: 2\nprincipals:\n  - uri_san: " + p106MappedURI + "\n    controller_type: direct_mtls\n    controller_id: tomasz.walczuk\n", mode: 0o600},
		{name: "unknown field", body: strings.Replace(validP106PrincipalMap, "version: 1", "version: 1\nallow_bearer: true", 1), mode: 0o600},
		{name: "wrong controller namespace", body: strings.Replace(validP106PrincipalMap, "direct_mtls", "queued_mac", 1), mode: 0o600},
		{name: "group readable", body: validP106PrincipalMap, mode: 0o640},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			options := writeP106TestFiles(t, root, pki, test.body)
			if err := os.Chmod(options.ClientPrincipalMap, test.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := NewDirectHTTPSServer(options); err == nil {
				t.Fatal("NewDirectHTTPSServer() accepted invalid principal map")
			}
		})
	}

	t.Run("missing server key", func(t *testing.T) {
		options := writeP106TestFiles(t, t.TempDir(), pki, validP106PrincipalMap)
		options.ServerPrivateKey = filepath.Join(t.TempDir(), "missing.key")
		if _, err := NewDirectHTTPSServer(options); err == nil {
			t.Fatal("NewDirectHTTPSServer() accepted missing server key")
		}
	})
	t.Run("missing client CA", func(t *testing.T) {
		options := writeP106TestFiles(t, t.TempDir(), pki, validP106PrincipalMap)
		options.ClientCA = filepath.Join(t.TempDir(), "missing-ca.pem")
		if _, err := NewDirectHTTPSServer(options); err == nil {
			t.Fatal("NewDirectHTTPSServer() accepted missing client CA")
		}
	})
	t.Run("missing principal map", func(t *testing.T) {
		options := writeP106TestFiles(t, t.TempDir(), pki, validP106PrincipalMap)
		options.ClientPrincipalMap = filepath.Join(t.TempDir(), "missing-map.yaml")
		if _, err := NewDirectHTTPSServer(options); err == nil {
			t.Fatal("NewDirectHTTPSServer() accepted missing principal map")
		}
	})
}

func TestP106DirectHTTPSPrincipalMapRejectsDuplicateURI(t *testing.T) {
	root := t.TempDir()
	pki := newP106TestPKI(t)
	duplicate := validP106PrincipalMap + "  - uri_san: " + p106MappedURI + "\n    controller_type: direct_mtls\n    controller_id: other\n"
	options := writeP106TestFiles(t, root, pki, duplicate)
	if _, err := NewDirectHTTPSServer(options); err == nil {
		t.Fatal("NewDirectHTTPSServer() accepted a duplicate URI SAN")
	}
}

const validP106PrincipalMap = `version: 1
principals:
  - uri_san: urn:remote-session-runner:controller:runner-tomasz-direct
    controller_type: direct_mtls
    controller_id: tomasz.walczuk
`

func writeP106TestFiles(t *testing.T, root string, pki p106TestPKI, principalMap string) DirectHTTPSServerOptions {
	t.Helper()
	paths := map[string][]byte{
		"server.pem":             pki.serverCert,
		"server.key":             pki.serverKey,
		"client-ca.pem":          pki.caPEM,
		"client-principals.yaml": []byte(principalMap),
	}
	for name, contents := range paths {
		if err := os.WriteFile(filepath.Join(root, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return DirectHTTPSServerOptions{
		BindAddress:        "127.0.0.1:0",
		ServerCertificate:  filepath.Join(root, "server.pem"),
		ServerPrivateKey:   filepath.Join(root, "server.key"),
		ClientCA:           filepath.Join(root, "client-ca.pem"),
		ClientPrincipalMap: filepath.Join(root, "client-principals.yaml"),
		Handler:            http.NotFoundHandler(),
	}
}

func p106HTTPClient(roots *x509.CertPool, clientCertificate *tls.Certificate, maxVersion uint16) *http.Client {
	minVersion := uint16(tls.VersionTLS13)
	if maxVersion < tls.VersionTLS13 {
		minVersion = maxVersion
	}
	config := &tls.Config{RootCAs: roots, MinVersion: minVersion, MaxVersion: maxVersion, ServerName: "127.0.0.1"}
	if clientCertificate != nil {
		config.Certificates = []tls.Certificate{*clientCertificate}
	}
	return &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: config}}
}

func newP106TestPKI(t *testing.T) p106TestPKI {
	t.Helper()
	now := time.Now().UTC()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "P106 Test CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	serverCert, serverKey, _ := p106IssueCertificate(t, now, caCert, caKey, "server", "", x509.ExtKeyUsageServerAuth)
	_, _, clientTLS := p106IssueCertificate(t, now, caCert, caKey, "client", p106MappedURI, x509.ExtKeyUsageClientAuth)
	_, _, otherClientTLS := p106IssueCertificate(t, now, caCert, caKey, "other-client", p106OtherMappedURI, x509.ExtKeyUsageClientAuth)
	_, _, unmappedTLS := p106IssueCertificate(t, now, caCert, caKey, "unmapped", "urn:remote-session-runner:controller:unmapped", x509.ExtKeyUsageClientAuth)
	otherCAKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherCATemplate := *caTemplate
	otherCATemplate.SerialNumber = big.NewInt(5)
	otherCATemplate.Subject = pkix.Name{CommonName: "Other P106 Test CA"}
	otherCADER, err := x509.CreateCertificate(rand.Reader, &otherCATemplate, &otherCATemplate, &otherCAKey.PublicKey, otherCAKey)
	if err != nil {
		t.Fatal(err)
	}
	otherCA, err := x509.ParseCertificate(otherCADER)
	if err != nil {
		t.Fatal(err)
	}
	_, _, unknownTLS := p106IssueCertificate(t, now, otherCA, otherCAKey, "unknown", p106MappedURI, x509.ExtKeyUsageClientAuth)
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	return p106TestPKI{
		caPEM:          caPEM,
		serverCert:     serverCert,
		serverKey:      serverKey,
		clientTLS:      clientTLS,
		otherClientTLS: otherClientTLS,
		unknownTLS:     unknownTLS,
		unmappedTLS:    unmappedTLS,
		serverRoots:    roots,
	}
}

func p106IssueCertificate(t *testing.T, now time.Time, ca *x509.Certificate, caKey *ecdsa.PrivateKey, commonName, uriSAN string, usage x509.ExtKeyUsage) ([]byte, []byte, tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	if commonName == "server" {
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		template.KeyUsage |= x509.KeyUsageKeyEncipherment
	}
	if uriSAN != "" {
		parsed, err := url.Parse(uriSAN)
		if err != nil {
			t.Fatal(err)
		}
		template.URIs = []*url.URL{parsed}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, keyPEM, tlsCert
}
