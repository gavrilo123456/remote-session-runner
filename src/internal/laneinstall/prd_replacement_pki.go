package laneinstall

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"

	"remote-session-runner/src/internal/config"
)

const (
	prdReplacementServerCommonName = "remote-session-runner:linux-host:prd-server"
	prdReplacementServerValidity   = 90 * 24 * time.Hour
)

// PRDReplacementServerCSR contains a key generated on the replacement host
// and a CSR bound to the one permitted replacement PRD identity. PrivateKeyPEM
// must remain on that host and be written with owner-only permissions.
type PRDReplacementServerCSR struct {
	PrivateKeyPEM []byte
	RequestPEM    []byte
}

// GeneratePRDReplacementServerCSR creates an ECDSA key and exact-SAN CSR for
// the replacement Linux PRD listener. It performs no filesystem or CA write.
func GeneratePRDReplacementServerCSR() (PRDReplacementServerCSR, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return PRDReplacementServerCSR{}, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return PRDReplacementServerCSR{}, err
	}
	requestDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:     pkix.Name{CommonName: prdReplacementServerCommonName},
		IPAddresses: []net.IP{net.ParseIP(config.SimpleReplacementLinuxPublicIP)},
	}, key)
	if err != nil {
		return PRDReplacementServerCSR{}, err
	}
	return PRDReplacementServerCSR{
		PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
		RequestPEM:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: requestDER}),
	}, nil
}

// SignPRDReplacementServerCSR signs only a CSR for the fixed replacement IP
// and common name. It requires the matching, currently-valid self-signed CA
// and returns a certificate verified for the TLS server role and IP identity.
// It does not persist the CA serial, key, request, or result.
func SignPRDReplacementServerCSR(requestPEM, caCertificatePEM, caPrivateKeyPEM []byte, now time.Time) ([]byte, string, error) {
	request, err := parseExactPRDReplacementCSR(requestPEM)
	if err != nil {
		return nil, "", err
	}
	ca, err := parsePRDReplacementRoot(caCertificatePEM, caPrivateKeyPEM, now)
	if err != nil {
		return nil, "", err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, "", err
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	notBefore := now.UTC().Add(-5 * time.Minute)
	notAfter := now.UTC().Add(prdReplacementServerValidity)
	if ca.certificate.NotAfter.Before(notAfter) {
		notAfter = ca.certificate.NotAfter
	}
	if !notAfter.After(now) {
		return nil, "", errors.New("replacement TLS root expires before a useful server certificate lifetime")
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: prdReplacementServerCommonName},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		IPAddresses:           []net.IP{net.ParseIP(config.SimpleReplacementLinuxPublicIP)},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.certificate, request.PublicKey, ca.signer)
	if err != nil {
		return nil, "", err
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil || validatePRDReplacementServerCertificate(certificate, ca.certificate, now) != nil {
		return nil, "", errors.New("issued replacement TLS certificate failed identity verification")
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}

// ValidatePRDReplacementServerMaterial verifies a transferred server
// certificate, its host-generated private key, and the public trust root used
// by Mac PRD. The CA private key is deliberately not needed on the worker.
// This validates material only; it does not write files or contact a service.
func ValidatePRDReplacementServerMaterial(certificatePEM, privateKeyPEM, caCertificatePEM []byte, now time.Time) error {
	certificateBlock, certificateRest := pem.Decode(certificatePEM)
	if certificateBlock == nil || certificateBlock.Type != "CERTIFICATE" || len(bytesTrimSpace(certificateRest)) != 0 {
		return errors.New("replacement server certificate must be one PEM certificate")
	}
	privateKeyBlock, privateKeyRest := pem.Decode(privateKeyPEM)
	if privateKeyBlock == nil || len(bytesTrimSpace(privateKeyRest)) != 0 {
		return errors.New("replacement server private key must be one PEM private key")
	}
	switch privateKeyBlock.Type {
	case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
	default:
		return errors.New("replacement server private key has an unsupported PEM type")
	}
	pair, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil || len(pair.Certificate) != 1 {
		return errors.New("replacement server certificate and private key do not form one exact pair")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return errors.New("replacement server certificate is invalid")
	}
	rootBlock, rest := pem.Decode(caCertificatePEM)
	if rootBlock == nil || rootBlock.Type != "CERTIFICATE" || len(bytesTrimSpace(rest)) != 0 {
		return errors.New("replacement public trust root must be one PEM certificate")
	}
	root, err := x509.ParseCertificate(rootBlock.Bytes)
	if err != nil || !root.IsCA || !root.BasicConstraintsValid || root.KeyUsage&x509.KeyUsageCertSign == 0 ||
		root.CheckSignatureFrom(root) != nil || now.Before(root.NotBefore) || !now.Before(root.NotAfter) {
		return errors.New("replacement public trust root is not a current self-signed CA")
	}
	return validatePRDReplacementServerCertificate(leaf, root, now)
}

type parsedPRDReplacementRoot struct {
	certificate *x509.Certificate
	signer      crypto.Signer
}

func parseExactPRDReplacementCSR(data []byte) (*x509.CertificateRequest, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(bytesTrimSpace(rest)) != 0 || len(data) > 16*1024 {
		return nil, errors.New("replacement CSR must be one bounded PEM request")
	}
	request, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || request.CheckSignature() != nil || request.Subject.CommonName != prdReplacementServerCommonName ||
		len(request.DNSNames) != 0 || len(request.EmailAddresses) != 0 || len(request.URIs) != 0 ||
		len(request.IPAddresses) != 1 || !request.IPAddresses[0].Equal(net.ParseIP(config.SimpleReplacementLinuxPublicIP)) {
		return nil, errors.New("replacement CSR has an unexpected key or subject alternative name")
	}
	return request, nil
}

func parsePRDReplacementRoot(certificatePEM, privateKeyPEM []byte, now time.Time) (parsedPRDReplacementRoot, error) {
	certBlock, certRest := pem.Decode(certificatePEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" || len(bytesTrimSpace(certRest)) != 0 {
		return parsedPRDReplacementRoot{}, errors.New("replacement trust root must be one PEM certificate")
	}
	certificate, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return parsedPRDReplacementRoot{}, errors.New("replacement trust root certificate is invalid")
	}
	if !certificate.IsCA || !certificate.BasicConstraintsValid || certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		return parsedPRDReplacementRoot{}, errors.New("replacement trust root is not a CA with certificate-signing usage")
	}
	if err := certificate.CheckSignatureFrom(certificate); err != nil {
		return parsedPRDReplacementRoot{}, fmt.Errorf("replacement trust root is not self-signed: %w", err)
	}
	if now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return parsedPRDReplacementRoot{}, errors.New("replacement trust root is outside its validity period")
	}
	keyBlock, keyRest := pem.Decode(privateKeyPEM)
	if keyBlock == nil || len(bytesTrimSpace(keyRest)) != 0 {
		return parsedPRDReplacementRoot{}, errors.New("replacement CA key must be one PEM private key")
	}
	var signer crypto.Signer
	switch keyBlock.Type {
	case "PRIVATE KEY":
		parsed, parseErr := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if parseErr == nil {
			signer, _ = parsed.(crypto.Signer)
		}
	case "RSA PRIVATE KEY":
		parsed, parseErr := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
		if parseErr == nil {
			signer = parsed
		}
	case "EC PRIVATE KEY":
		parsed, parseErr := x509.ParseECPrivateKey(keyBlock.Bytes)
		if parseErr == nil {
			signer = parsed
		}
	}
	if signer == nil || !samePublicKey(signer.Public(), certificate.PublicKey) {
		return parsedPRDReplacementRoot{}, errors.New("replacement CA signing key does not match its certificate")
	}
	return parsedPRDReplacementRoot{certificate: certificate, signer: signer}, nil
}

func validatePRDReplacementServerCertificate(certificate, ca *x509.Certificate, now time.Time) error {
	if certificate == nil || ca == nil || certificate.Subject.CommonName != prdReplacementServerCommonName ||
		certificate.IsCA || !certificate.BasicConstraintsValid || len(certificate.IPAddresses) != 1 ||
		!certificate.IPAddresses[0].Equal(net.ParseIP(config.SimpleReplacementLinuxPublicIP)) ||
		len(certificate.DNSNames) != 0 || len(certificate.EmailAddresses) != 0 || len(certificate.URIs) != 0 ||
		certificate.KeyUsage&x509.KeyUsageDigitalSignature == 0 || len(certificate.ExtKeyUsage) != 1 ||
		certificate.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
		return errors.New("replacement server certificate identity or usage is invalid")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSName: config.SimpleReplacementLinuxPublicIP}); err != nil {
		return fmt.Errorf("verify replacement server certificate: %w", err)
	}
	return nil
}

func samePublicKey(left, right any) bool {
	leftDER, leftErr := x509.MarshalPKIXPublicKey(left)
	rightDER, rightErr := x509.MarshalPKIXPublicKey(right)
	return leftErr == nil && rightErr == nil && string(leftDER) == string(rightDER)
}

func bytesTrimSpace(value []byte) []byte {
	start, end := 0, len(value)
	for start < end && (value[start] == ' ' || value[start] == '\n' || value[start] == '\r' || value[start] == '\t') {
		start++
	}
	for end > start && (value[end-1] == ' ' || value[end-1] == '\n' || value[end-1] == '\r' || value[end-1] == '\t') {
		end--
	}
	return value[start:end]
}
