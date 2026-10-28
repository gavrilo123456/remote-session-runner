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
	"testing"
	"time"
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
