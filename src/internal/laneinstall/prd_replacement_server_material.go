package laneinstall

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
	"remote-session-runner/src/internal/config"
)

const prdReplacementServerMaterialFormatVersion = 1

// PRDReplacementServerMaterialReport contains public certificate material.
// The operation-specific CA signing key remains owner-only on the Mac.
type PRDReplacementServerMaterialReport struct {
	OperationID    string `json:"operation_id"`
	RequestSHA256  string `json:"request_sha256"`
	CertificatePEM []byte `json:"server_certificate_pem"`
	CertificateSHA string `json:"server_certificate_sha256"`
	CAPEM          []byte `json:"server_ca_pem"`
	CASHA          string `json:"server_ca_sha256"`
}

type prdReplacementServerMaterialManifest struct {
	FormatVersion  int    `json:"format_version"`
	OperationID    string `json:"operation_id"`
	RequestSHA256  string `json:"request_sha256"`
	CertificateSHA string `json:"server_certificate_sha256"`
	CASHA          string `json:"server_ca_sha256"`
	CAKeySHA256    string `json:"server_ca_private_key_sha256"`
	CreatedAtUTC   string `json:"created_at_utc"`
}

// PreparePRDReplacementServerMaterial signs the replacement-host CSR with an
// operation-specific CA whose private key remains in the Mac's owner-only
// workflow directory. Repeating the same operation and CSR returns identical
// stored material; a different CSR under that operation is rejected.
func PreparePRDReplacementServerMaterial(operationID string, requestPEM []byte, now time.Time) (PRDReplacementServerMaterialReport, error) {
	if runtime.GOOS != "darwin" || !validCatalogOperationID(operationID) || now.IsZero() {
		return PRDReplacementServerMaterialReport{}, ErrInvalidPRDUpgradePlan
	}
	current, err := user.Current()
	if err != nil || current.Username != config.MacAccount {
		return PRDReplacementServerMaterialReport{}, ErrInvalidPRDUpgradePlan
	}
	root, err := PRDReplacementBootstrapJournalRoot()
	if err != nil {
		return PRDReplacementServerMaterialReport{}, err
	}
	return preparePRDReplacementServerMaterialAt(root, operationID, requestPEM, now)
}

func preparePRDReplacementServerMaterialAt(root, operationID string, requestPEM []byte, now time.Time) (PRDReplacementServerMaterialReport, error) {
	var result PRDReplacementServerMaterialReport
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !validCatalogOperationID(operationID) || now.IsZero() {
		return result, ErrCatalogOperationInvalid
	}
	if _, err := parseExactPRDReplacementCSR(requestPEM); err != nil {
		return result, err
	}
	if err := ensureBootstrapJournalRootAtRoot(root); err != nil {
		return result, err
	}
	operationRoot := filepath.Join(root, "server-material")
	if err := ensureOwnerDirectoryChainFrom(filepath.Dir(root), operationRoot); err != nil {
		return result, err
	}
	lock, err := acquireBootstrapLock(filepath.Join(operationRoot, operationID+".lock"))
	if err != nil {
		return result, err
	}
	defer releaseBootstrapLock(lock)
	operationDir := filepath.Join(operationRoot, operationID)
	if _, err := os.Lstat(operationDir); err == nil {
		return readPRDReplacementServerMaterialAt(operationRoot, operationID, digestBytes(requestPEM))
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	caCertificatePEM, caPrivateKeyPEM, err := generatePRDReplacementServerCA(now)
	if err != nil {
		return result, err
	}
	if _, err := parsePRDReplacementRoot(caCertificatePEM, caPrivateKeyPEM, now); err != nil {
		return result, fmt.Errorf("validate generated replacement server CA: %w", err)
	}
	certificatePEM, certificateSHA, err := SignPRDReplacementServerCSR(requestPEM, caCertificatePEM, caPrivateKeyPEM, now)
	if err != nil {
		return result, err
	}
	// The CSR public key is the replacement host's private server key. Confirm
	// the issued certificate contains precisely that key before persisting it.
	request, err := parseExactPRDReplacementCSR(requestPEM)
	if err != nil {
		return result, err
	}
	certificateBlock, _ := pem.Decode(certificatePEM)
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil || !samePublicKey(certificate.PublicKey, request.PublicKey) {
		return result, ErrInvalidLaneTLSMaterial
	}
	caKeyDigest := digestBytes(caPrivateKeyPEM)
	manifest := prdReplacementServerMaterialManifest{FormatVersion: prdReplacementServerMaterialFormatVersion,
		OperationID: operationID, RequestSHA256: digestBytes(requestPEM), CertificateSHA: certificateSHA,
		CASHA: digestBytes(caCertificatePEM), CAKeySHA256: caKeyDigest, CreatedAtUTC: now.UTC().Format(time.RFC3339Nano)}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return result, err
	}
	manifestBytes = append(manifestBytes, '\n')
	stage, err := os.MkdirTemp(operationRoot, ".server-material-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o700); err != nil {
		return result, err
	}
	for name, contents := range map[string][]byte{
		"manifest.json": manifestBytes, "server.csr": requestPEM,
		"server.pem": certificatePEM, "server-ca.pem": caCertificatePEM,
		"server-ca.key": caPrivateKeyPEM,
	} {
		if err := writePRDReplacementCSRFile(filepath.Join(stage, name), contents); err != nil {
			return result, err
		}
	}
	if err := syncDirectory(stage); err != nil {
		return result, err
	}
	if _, err := os.Lstat(operationDir); !errors.Is(err, os.ErrNotExist) {
		return result, ErrCatalogOperationInvalid
	}
	if err := os.Rename(stage, operationDir); err != nil {
		return result, err
	}
	if err := syncDirectory(operationRoot); err != nil {
		return result, err
	}
	return readPRDReplacementServerMaterialAt(operationRoot, operationID, manifest.RequestSHA256)
}

func readPRDReplacementServerMaterialAt(root, operationID, requestSHA256 string) (PRDReplacementServerMaterialReport, error) {
	var result PRDReplacementServerMaterialReport
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !validCatalogOperationID(operationID) ||
		!bundleDigestPattern.MatchString(requestSHA256) {
		return result, ErrCatalogOperationInvalid
	}
	operationDir := filepath.Join(root, operationID)
	info, err := os.Lstat(operationDir)
	var stat unix.Stat_t
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 ||
		unix.Lstat(operationDir, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Nlink < 2 {
		return result, ErrCatalogOperationInvalid
	}
	for _, name := range []string{"manifest.json", "server.csr", "server.pem", "server-ca.pem", "server-ca.key"} {
		if err := verifyPRDReplacementCSRFile(filepath.Join(operationDir, name), 1<<20); err != nil {
			return result, err
		}
	}
	manifestBytes, err := os.ReadFile(filepath.Join(operationDir, "manifest.json"))
	if err != nil {
		return result, err
	}
	var manifest prdReplacementServerMaterialManifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return result, ErrCatalogOperationInvalid
	}
	canonical, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || !bytes.Equal(manifestBytes, append(canonical, '\n')) ||
		manifest.FormatVersion != prdReplacementServerMaterialFormatVersion || manifest.OperationID != operationID ||
		manifest.RequestSHA256 != requestSHA256 || !bundleDigestPattern.MatchString(manifest.CertificateSHA) ||
		!bundleDigestPattern.MatchString(manifest.CASHA) || !bundleDigestPattern.MatchString(manifest.CAKeySHA256) ||
		!validCohortTimestamp(manifest.CreatedAtUTC) {
		return result, ErrCatalogOperationInvalid
	}
	requestPEM, err := os.ReadFile(filepath.Join(operationDir, "server.csr"))
	if err != nil || digestBytes(requestPEM) != manifest.RequestSHA256 {
		return result, ErrCatalogOperationInvalid
	}
	certificatePEM, err := os.ReadFile(filepath.Join(operationDir, "server.pem"))
	if err != nil || digestBytes(certificatePEM) != manifest.CertificateSHA {
		return result, ErrCatalogOperationInvalid
	}
	caCertificatePEM, err := os.ReadFile(filepath.Join(operationDir, "server-ca.pem"))
	if err != nil || digestBytes(caCertificatePEM) != manifest.CASHA {
		return result, ErrCatalogOperationInvalid
	}
	caPrivateKeyPEM, err := os.ReadFile(filepath.Join(operationDir, "server-ca.key"))
	if err != nil || digestBytes(caPrivateKeyPEM) != manifest.CAKeySHA256 {
		return result, ErrCatalogOperationInvalid
	}
	if err := validatePRDReplacementIssuedMaterial(requestPEM, certificatePEM, caCertificatePEM, caPrivateKeyPEM, time.Now().UTC()); err != nil {
		return result, err
	}
	return PRDReplacementServerMaterialReport{OperationID: operationID, RequestSHA256: manifest.RequestSHA256,
		CertificatePEM: append([]byte(nil), certificatePEM...), CertificateSHA: manifest.CertificateSHA,
		CAPEM: append([]byte(nil), caCertificatePEM...), CASHA: manifest.CASHA}, nil
}

func validatePRDReplacementIssuedMaterial(requestPEM, certificatePEM, caCertificatePEM, caPrivateKeyPEM []byte, now time.Time) error {
	root, err := parsePRDReplacementRoot(caCertificatePEM, caPrivateKeyPEM, now)
	if err != nil {
		return err
	}
	request, err := parseExactPRDReplacementCSR(requestPEM)
	if err != nil {
		return err
	}
	block, rest := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return ErrInvalidLaneTLSMaterial
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !samePublicKey(certificate.PublicKey, request.PublicKey) ||
		validatePRDReplacementServerCertificate(certificate, root.certificate, now) != nil {
		return ErrInvalidLaneTLSMaterial
	}
	return nil
}

func generatePRDReplacementServerCA(now time.Time) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, nil, err
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	template := &x509.Certificate{SerialNumber: serial,
		Subject:   pkix.Name{CommonName: "remote-session-runner:linux-host:prd-server-ca"},
		NotBefore: now.UTC().Add(-5 * time.Minute), NotAfter: now.UTC().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true, IsCA: true, MaxPathLenZero: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), nil
}

func digestPRDReplacementServerMaterial(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
