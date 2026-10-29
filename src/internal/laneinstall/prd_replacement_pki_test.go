package laneinstall

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
)

func TestGenerateAndSignPRDReplacementServerCSR(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	rootPEM, keyPEM := prdReplacementTestCA(t, now)
	request, err := GeneratePRDReplacementServerCSR()
	if err != nil {
		t.Fatalf("generate replacement CSR: %v", err)
	}
	certificatePEM, digest, err := SignPRDReplacementServerCSR(request.RequestPEM, rootPEM, keyPEM, now)
	if err != nil {
		t.Fatalf("sign exact replacement CSR: %v", err)
	}
	if !validConfigDigest(digest) {
		t.Fatalf("certificate digest is invalid: %q", digest)
	}
	block, rest := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" || len(bytesTrimSpace(rest)) != 0 {
		t.Fatal("signer did not return one certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	rootBlock, _ := pem.Decode(rootPEM)
	root, rootErr := x509.ParseCertificate(rootBlock.Bytes)
	if err != nil || rootErr != nil || validatePRDReplacementServerCertificate(certificate, root, now) != nil {
		t.Fatalf("issued certificate does not validate for replacement TLS: certErr=%v rootErr=%v", err, rootErr)
	}
}

func TestValidatePRDReplacementServerMaterial(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	rootPEM, rootKeyPEM := prdReplacementTestCA(t, now)
	request, err := GeneratePRDReplacementServerCSR()
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM, _, err := SignPRDReplacementServerCSR(request.RequestPEM, rootPEM, rootKeyPEM, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePRDReplacementServerMaterial(certificatePEM, request.PrivateKeyPEM, rootPEM, now); err != nil {
		t.Fatalf("validate exact replacement server material: %v", err)
	}

	_, wrongKeyPEM := prdReplacementTestCA(t, now)
	otherRootPEM, _ := prdReplacementTestCA(t, now)
	cases := []struct {
		name                          string
		certificate, privateKey, root []byte
		at                            time.Time
	}{
		{name: "mismatched private key", certificate: certificatePEM, privateKey: wrongKeyPEM, root: rootPEM, at: now},
		{name: "wrong trust root", certificate: certificatePEM, privateKey: request.PrivateKeyPEM, root: otherRootPEM, at: now},
		{name: "trailing certificate data", certificate: append(append([]byte(nil), certificatePEM...), []byte("unexpected")...), privateKey: request.PrivateKeyPEM, root: rootPEM, at: now},
		{name: "trailing private key data", certificate: certificatePEM, privateKey: append(append([]byte(nil), request.PrivateKeyPEM...), []byte("unexpected")...), root: rootPEM, at: now},
		{name: "expired leaf", certificate: certificatePEM, privateKey: request.PrivateKeyPEM, root: rootPEM, at: now.Add(prdReplacementServerValidity + time.Hour)},
		{name: "expired trust root", certificate: certificatePEM, privateKey: request.PrivateKeyPEM, root: rootPEM, at: now.Add(366 * 24 * time.Hour)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidatePRDReplacementServerMaterial(test.certificate, test.privateKey, test.root, test.at); err == nil {
				t.Fatal("accepted invalid replacement server material")
			}
		})
	}
}

func TestSignPRDReplacementServerCSRRejectsWrongIdentityAndAuthority(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	rootPEM, keyPEM := prdReplacementTestCA(t, now)
	request, err := GeneratePRDReplacementServerCSR()
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(request.PrivateKeyPEM)
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := parsed.(*ecdsa.PrivateKey)
	wrongCSR, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:     pkix.Name{CommonName: prdReplacementServerCommonName},
		IPAddresses: []net.IP{net.ParseIP("79.72.94.60")},
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	wrongCSRPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: wrongCSR})
	if _, _, err := SignPRDReplacementServerCSR(wrongCSRPEM, rootPEM, keyPEM, now); err == nil {
		t.Fatal("signer accepted a CSR with a different IP SAN")
	}

	otherRoot, otherKey := prdReplacementTestCA(t, now)
	if _, _, err := SignPRDReplacementServerCSR(request.RequestPEM, rootPEM, otherKey, now); err == nil {
		t.Fatal("signer accepted a private key that does not match the configured CA")
	}
	if _, _, err := SignPRDReplacementServerCSR(request.RequestPEM, otherRoot, keyPEM, now); err == nil {
		t.Fatal("signer accepted a different CA certificate")
	}
	if _, _, err := SignPRDReplacementServerCSR(append(request.RequestPEM, []byte("extra")...), rootPEM, keyPEM, now); err == nil {
		t.Fatal("signer accepted trailing data after the CSR")
	}
	if _, _, err := SignPRDReplacementServerCSR(request.RequestPEM, rootPEM, keyPEM, now.Add(400*24*time.Hour)); err == nil {
		t.Fatal("signer accepted an expired CA")
	}
}

func TestValidatePRDReplacementLegacyClientMaterial(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	caPEM, caKeyPEM := prdReplacementTestCA(t, now)
	certificatePEM := prdReplacementLegacyClientCert(t, now, caPEM, caKeyPEM, prdReplacementLegacyDirectURI, x509.ExtKeyUsageClientAuth)
	principalMap := []byte("version: 1\nprincipals:\n  - uri_san: " + prdReplacementLegacyDirectURI + "\n    controller_type: direct_mtls\n    controller_id: " + config.MacAccount + "\n")
	if err := validatePRDReplacementLegacyClientMaterial(certificatePEM, caPEM, principalMap, now); err != nil {
		t.Fatalf("valid existing V2 client identity rejected: %v", err)
	}

	tests := []struct {
		name    string
		cert    []byte
		mapping []byte
		at      time.Time
	}{
		{name: "wrong certificate URI", cert: prdReplacementLegacyClientCert(t, now, caPEM, caKeyPEM, "urn:remote-session-runner:lane:prd:controller:linux-host", x509.ExtKeyUsageClientAuth), mapping: principalMap, at: now},
		{name: "wrong mapped identity", cert: certificatePEM, mapping: []byte("version: 1\nprincipals:\n  - uri_san: " + prdReplacementLegacyDirectURI + "\n    controller_type: direct_mtls\n    controller_id: another-user\n"), at: now},
		{name: "wrong usage", cert: prdReplacementLegacyClientCert(t, now, caPEM, caKeyPEM, prdReplacementLegacyDirectURI, x509.ExtKeyUsageServerAuth), mapping: principalMap, at: now},
		{name: "expired", cert: certificatePEM, mapping: principalMap, at: now.Add(48 * time.Hour)},
		{name: "trailing certificate data", cert: append(append([]byte(nil), certificatePEM...), []byte("extra")...), mapping: principalMap, at: now},
		{name: "trailing map document", cert: certificatePEM, mapping: append(append([]byte(nil), principalMap...), []byte("---\nversion: 1\nprincipals: []\n")...), at: now},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePRDReplacementLegacyClientMaterial(test.cert, caPEM, test.mapping, test.at); err == nil {
				t.Fatal("invalid legacy client identity was accepted")
			}
		})
	}
}

func prdReplacementLegacyClientCert(t *testing.T, now time.Time, caPEM, caKeyPEM []byte, uriValue string, usage x509.ExtKeyUsage) []byte {
	t.Helper()
	rootBlock, _ := pem.Decode(caPEM)
	keyBlock, _ := pem.Decode(caKeyPEM)
	if rootBlock == nil || keyBlock == nil {
		t.Fatal("test CA fixture is invalid")
	}
	root, err := x509.ParseCertificate(rootBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(uriValue)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(11), Subject: pkix.Name{CommonName: "runner-tomasz-direct"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		BasicConstraintsValid: true, IsCA: false, URIs: []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, root, &clientKey.PublicKey, parsedKey.(*ecdsa.PrivateKey))
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func prdReplacementTestCA(t *testing.T, now time.Time) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(7),
		Subject:               pkix.Name{CommonName: "test replacement CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
}
