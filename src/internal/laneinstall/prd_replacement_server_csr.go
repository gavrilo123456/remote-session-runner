package laneinstall

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
	"remote-session-runner/src/internal/config"
)

const prdReplacementServerCSRFormatVersion = 1

type PRDReplacementServerCSRReport struct {
	OperationID string `json:"operation_id"`
	RequestPEM  []byte `json:"request_pem"`
	RequestSHA  string `json:"request_sha256"`
}

type prdReplacementServerCSRManifest struct {
	FormatVersion    int    `json:"format_version"`
	OperationID      string `json:"operation_id"`
	RequestSHA256    string `json:"request_sha256"`
	PrivateKeySHA256 string `json:"private_key_sha256"`
	CreatedAtUTC     string `json:"created_at_utc"`
}

// PreparePRDReplacementServerCSR creates or returns the operation-bound CSR
// on the replacement Linux host. The private key remains in its owner-only
// workflow directory and is never returned by this command.
func PreparePRDReplacementServerCSR(operationID string, now time.Time) (PRDReplacementServerCSRReport, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || !validCatalogOperationID(operationID) || now.IsZero() {
		return PRDReplacementServerCSRReport{}, ErrInvalidPRDUpgradePlan
	}
	current, err := user.Current()
	if err != nil || current.Username != config.LinuxAccount || !replacementLinuxLocalIdentityMatches() {
		return PRDReplacementServerCSRReport{}, ErrInvalidPRDUpgradePlan
	}
	root, err := PRDReplacementBootstrapJournalRoot()
	if err != nil {
		return PRDReplacementServerCSRReport{}, err
	}
	return preparePRDReplacementServerCSRAt(root, operationID, now)
}

func preparePRDReplacementServerCSRAt(root, operationID string, now time.Time) (PRDReplacementServerCSRReport, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !validCatalogOperationID(operationID) || now.IsZero() {
		return PRDReplacementServerCSRReport{}, ErrCatalogOperationInvalid
	}
	if err := ensureBootstrapJournalRootAtRoot(root); err != nil {
		return PRDReplacementServerCSRReport{}, err
	}
	lock, err := acquireBootstrapLock(filepath.Join(root, operationID+"-server-csr.lock"))
	if err != nil {
		return PRDReplacementServerCSRReport{}, err
	}
	defer releaseBootstrapLock(lock)

	operationRoot := filepath.Join(root, "server-csr")
	if err := ensureOwnerDirectoryChainFrom(filepath.Dir(root), operationRoot); err != nil {
		return PRDReplacementServerCSRReport{}, err
	}
	operationDir := filepath.Join(operationRoot, operationID)
	if _, err := os.Lstat(operationDir); err == nil {
		return readPRDReplacementServerCSRAt(operationRoot, operationID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return PRDReplacementServerCSRReport{}, err
	}
	generated, err := GeneratePRDReplacementServerCSR()
	if err != nil {
		return PRDReplacementServerCSRReport{}, err
	}
	if err := validatePRDReplacementServerCSRKeyPair(generated.RequestPEM, generated.PrivateKeyPEM); err != nil {
		return PRDReplacementServerCSRReport{}, err
	}
	stage, err := os.MkdirTemp(operationRoot, ".server-csr-*")
	if err != nil {
		return PRDReplacementServerCSRReport{}, err
	}
	defer os.RemoveAll(stage)
	if err := os.Chmod(stage, 0o700); err != nil {
		return PRDReplacementServerCSRReport{}, err
	}
	manifest := prdReplacementServerCSRManifest{FormatVersion: prdReplacementServerCSRFormatVersion,
		OperationID: operationID, RequestSHA256: digestBytes(generated.RequestPEM),
		PrivateKeySHA256: digestBytes(generated.PrivateKeyPEM), CreatedAtUTC: now.UTC().Format(time.RFC3339Nano)}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return PRDReplacementServerCSRReport{}, err
	}
	manifestBytes = append(manifestBytes, '\n')
	for name, contents := range map[string][]byte{
		"server.key":    generated.PrivateKeyPEM,
		"server.csr":    generated.RequestPEM,
		"manifest.json": manifestBytes,
	} {
		if err := writePRDReplacementCSRFile(filepath.Join(stage, name), contents); err != nil {
			return PRDReplacementServerCSRReport{}, err
		}
	}
	if err := syncDirectory(stage); err != nil {
		return PRDReplacementServerCSRReport{}, err
	}
	if _, err := os.Lstat(operationDir); !errors.Is(err, os.ErrNotExist) {
		return PRDReplacementServerCSRReport{}, ErrCatalogOperationInvalid
	}
	if err := os.Rename(stage, operationDir); err != nil {
		return PRDReplacementServerCSRReport{}, err
	}
	if err := syncDirectory(operationRoot); err != nil {
		return PRDReplacementServerCSRReport{}, err
	}
	return PRDReplacementServerCSRReport{OperationID: operationID,
		RequestPEM: append([]byte(nil), generated.RequestPEM...), RequestSHA: manifest.RequestSHA256}, nil
}

func readPRDReplacementServerCSRAt(root, operationID string) (PRDReplacementServerCSRReport, error) {
	var result PRDReplacementServerCSRReport
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !validCatalogOperationID(operationID) {
		return result, ErrCatalogOperationInvalid
	}
	directory := filepath.Join(root, operationID)
	info, err := os.Lstat(directory)
	var stat unix.Stat_t
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 ||
		unix.Lstat(directory, &stat) != nil || stat.Uid != uint32(os.Getuid()) || stat.Nlink < 2 {
		return result, ErrCatalogOperationInvalid
	}
	for _, name := range []string{"manifest.json", "server.csr", "server.key"} {
		if err := verifyPRDReplacementCSRFile(filepath.Join(directory, name), 1<<20); err != nil {
			return result, err
		}
	}
	manifestBytes, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return result, err
	}
	var manifest prdReplacementServerCSRManifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return result, ErrCatalogOperationInvalid
	}
	canonical, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil || !bytes.Equal(manifestBytes, append(canonical, '\n')) ||
		manifest.FormatVersion != prdReplacementServerCSRFormatVersion || manifest.OperationID != operationID ||
		!bundleDigestPattern.MatchString(manifest.RequestSHA256) || !bundleDigestPattern.MatchString(manifest.PrivateKeySHA256) ||
		!validCohortTimestamp(manifest.CreatedAtUTC) {
		return result, ErrCatalogOperationInvalid
	}
	requestPEM, err := os.ReadFile(filepath.Join(directory, "server.csr"))
	if err != nil || digestBytes(requestPEM) != manifest.RequestSHA256 {
		return result, ErrCatalogOperationInvalid
	}
	privateKeyPEM, err := os.ReadFile(filepath.Join(directory, "server.key"))
	if err != nil || digestBytes(privateKeyPEM) != manifest.PrivateKeySHA256 ||
		validatePRDReplacementServerCSRKeyPair(requestPEM, privateKeyPEM) != nil {
		return result, ErrCatalogOperationInvalid
	}
	return PRDReplacementServerCSRReport{OperationID: operationID,
		RequestPEM: append([]byte(nil), requestPEM...), RequestSHA: manifest.RequestSHA256}, nil
}

func loadPRDReplacementServerKey(operationID, requestSHA256 string) ([]byte, []byte, error) {
	root, err := PRDReplacementBootstrapJournalRoot()
	if err != nil || !bundleDigestPattern.MatchString(requestSHA256) {
		return nil, nil, ErrCatalogOperationInvalid
	}
	return loadPRDReplacementServerKeyAt(root, operationID, requestSHA256)
}

func loadPRDReplacementServerKeyAt(root, operationID, requestSHA256 string) ([]byte, []byte, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !bundleDigestPattern.MatchString(requestSHA256) {
		return nil, nil, ErrCatalogOperationInvalid
	}
	report, err := readPRDReplacementServerCSRAt(filepath.Join(root, "server-csr"), operationID)
	if err != nil || report.RequestSHA != requestSHA256 {
		return nil, nil, errors.Join(ErrCatalogOperationInvalid, err)
	}
	privateKey, err := os.ReadFile(filepath.Join(root, "server-csr", operationID, "server.key"))
	if err != nil {
		return nil, nil, err
	}
	return report.RequestPEM, privateKey, nil
}

func validatePRDReplacementServerCSRKeyPair(requestPEM, privateKeyPEM []byte) error {
	request, err := parseExactPRDReplacementCSR(requestPEM)
	if err != nil {
		return err
	}
	block, rest := pem.Decode(privateKeyPEM)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return ErrInvalidLaneTLSMaterial
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return ErrInvalidLaneTLSMaterial
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return ErrInvalidLaneTLSMaterial
	}
	requestPublic, err := x509.MarshalPKIXPublicKey(request.PublicKey)
	if err != nil {
		return ErrInvalidLaneTLSMaterial
	}
	keyPublic, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(requestPublic, keyPublic) {
		return ErrInvalidLaneTLSMaterial
	}
	return nil
}

func writePRDReplacementCSRFile(path string, contents []byte) error {
	if len(contents) == 0 || len(contents) > 1<<20 {
		return ErrCatalogOperationInvalid
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return verifyPRDReplacementCSRFile(path, 1<<20)
}

func verifyPRDReplacementCSRFile(path string, limit int64) error {
	info, err := os.Lstat(path)
	var stat unix.Stat_t
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 ||
		info.Size() <= 0 || info.Size() > limit || unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Getuid()) ||
		stat.Gid != uint32(os.Getgid()) || stat.Nlink != 1 {
		return ErrCatalogOperationInvalid
	}
	return nil
}

func digestPRDReplacementCSR(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
