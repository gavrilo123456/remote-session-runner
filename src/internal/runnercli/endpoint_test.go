package runnercli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/runnerclient"
)

func TestP119EndpointResolverKeepsTheExplicitProfile(t *testing.T) {
	remoteClient := &fakeSessionClient{kind: runnerclient.EndpointHTTPS}
	profileReads := 0
	resolver := defaultEndpointResolver{
		readRemoteProfile: func(name, configPath string) (remoteEndpointProfile, error) {
			profileReads++
			if name != "linux-poc" || configPath != "/test/mac.yaml" {
				return remoteEndpointProfile{}, ErrEndpointProfile
			}
			return remoteEndpointProfile{name: "linux-poc", endpoint: config.PublicEndpoint}, nil
		},
		newHTTPSClient: func(profile remoteEndpointProfile) (sessionClient, error) {
			if profile.name != "linux-poc" || profile.endpoint != config.PublicEndpoint {
				t.Fatalf("unexpected HTTPS profile: %+v", profile)
			}
			return remoteClient, nil
		},
	}

	local, err := resolver.Resolve("local", "/ignored/mac.yaml")
	if err != nil || local.EndpointKind() != runnerclient.EndpointUnixSocket {
		t.Fatalf("local endpoint client=%v err=%v", local, err)
	}
	if profileReads != 0 {
		t.Fatalf("local endpoint unexpectedly loaded remote profile %d times", profileReads)
	}

	remote, err := resolver.Resolve("linux-poc", "/test/mac.yaml")
	if err != nil || remote != remoteClient || remote.EndpointKind() != runnerclient.EndpointHTTPS {
		t.Fatalf("remote endpoint client=%v err=%v", remote, err)
	}
	if profileReads != 1 {
		t.Fatalf("remote profile reads=%d, want 1", profileReads)
	}

	if _, err := resolver.Resolve(config.PublicEndpoint, "/test/mac.yaml"); err == nil {
		t.Fatal("raw URL unexpectedly selected an endpoint profile")
	}
}

func TestP119MacRemoteProfileRequiresSelectedServerCA(t *testing.T) {
	profile, err := remoteProfileFromSettings("linux-poc", config.HostKindMac, config.MacSettings{
		RemoteEndpointProfile:   "linux-poc",
		RemoteEndpoint:          config.PublicEndpoint,
		RemoteServerCA:          filepath.Join(config.MacServiceRoot, "secrets/poc-ca.pem"),
		DirectClientCertificate: filepath.Join(config.MacServiceRoot, "secrets/direct-client.pem"),
	}, config.SecretReference{File: filepath.Join(config.MacServiceRoot, "secrets/direct-client.key")}, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if profile.name != "linux-poc" || profile.endpoint != config.PublicEndpoint ||
		profile.serverCA != filepath.Join(config.MacServiceRoot, "secrets/poc-ca.pem") ||
		profile.clientCert != filepath.Join(config.MacServiceRoot, "secrets/direct-client.pem") ||
		profile.clientKey != filepath.Join(config.MacServiceRoot, "secrets/direct-client.key") {
		t.Fatalf("selected mTLS profile was not preserved: %+v", profile)
	}

	if _, err := remoteProfileFromSettings("other", config.HostKindMac, config.MacSettings{
		RemoteEndpointProfile: "linux-poc", RemoteEndpoint: config.PublicEndpoint,
		RemoteServerCA:          filepath.Join(config.MacServiceRoot, "secrets/poc-ca.pem"),
		DirectClientCertificate: filepath.Join(config.MacServiceRoot, "secrets/direct-client.pem"),
	}, config.SecretReference{File: filepath.Join(config.MacServiceRoot, "secrets/direct-client.key")}, true, true); err == nil {
		t.Fatal("unconfigured profile name unexpectedly resolved")
	}
	if _, err := remoteProfileFromSettings("linux-poc", config.HostKindLinux, config.MacSettings{}, config.SecretReference{}, false, false); err == nil {
		t.Fatal("Linux config unexpectedly supplied a Mac CLI endpoint profile")
	}
}

func TestP119ProfileHTTPSClientLoadsOwnerOnlyMTLSFiles(t *testing.T) {
	caPEM, clientCertPEM, privateKeyPEM := p119ClientCredentialFixture(t)
	secrets := t.TempDir()
	caPath := filepath.Join(secrets, "poc-ca.pem")
	certPath := filepath.Join(secrets, "direct-client.pem")
	keyPath := filepath.Join(secrets, "direct-client.key")
	for path, value := range map[string][]byte{caPath: caPEM, certPath: clientCertPEM, keyPath: privateKeyPEM} {
		if err := os.WriteFile(path, value, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	client, err := newProfileHTTPSClient(remoteEndpointProfile{
		name: "linux-poc", endpoint: "https://127.0.0.1:8443", serverCA: caPath,
		clientCert: certPath, clientKey: keyPath,
	})
	if err != nil || client.EndpointKind() != runnerclient.EndpointHTTPS {
		t.Fatalf("owner-only mTLS profile client=%v err=%v", client, err)
	}

	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = newProfileHTTPSClient(remoteEndpointProfile{
		name: "linux-poc", endpoint: "https://127.0.0.1:8443", serverCA: caPath,
		clientCert: certPath, clientKey: keyPath,
	})
	if !errors.Is(err, ErrEndpointProfile) {
		t.Fatalf("group-readable private key error=%v, want ErrEndpointProfile", err)
	}
	if strings.Contains(err.Error(), string(privateKeyPEM)) {
		t.Fatal("profile error exposed private-key bytes")
	}
}

func TestP119CredentialReaderRejectsSymlinkAndOversize(t *testing.T) {
	dir := t.TempDir()
	credential := filepath.Join(dir, "credential")
	link := filepath.Join(dir, "credential-link")
	if err := os.WriteFile(credential, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(credential, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readOwnerOnlyCredential(link); !errors.Is(err, ErrEndpointProfile) {
		t.Fatalf("symlink credential error=%v, want ErrEndpointProfile", err)
	}
	if err := os.WriteFile(credential, make([]byte, maxCredentialFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOwnerOnlyCredential(credential); !errors.Is(err, ErrEndpointProfile) {
		t.Fatalf("oversize credential error=%v, want ErrEndpointProfile", err)
	}
}

func p119ClientCredentialFixture(t *testing.T) ([]byte, []byte, []byte) {
	t.Helper()
	now := time.Now()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(11), Subject: pkix.Name{CommonName: "P119 test CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(12), Subject: pkix.Name{CommonName: "P119 test client"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caTemplate, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyDER, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	clientCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateKeyDER})
	return caPEM, clientCertPEM, privateKeyPEM
}
