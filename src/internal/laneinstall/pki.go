package laneinstall

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrInvalidLanePKI = errors.New("invalid lane PKI material")

type PKILeafRole string

const (
	PKIReleaseTransportServer  PKILeafRole = "release_transport_server"
	PKIReleaseAuthorityClient  PKILeafRole = "release_authority_client"
	PKIReleaseTransportProfile             = "release-transport-v1"
	maxPKILeafValidity                     = 90 * 24 * time.Hour
	maxPKIOverlapValidity                  = 14 * 24 * time.Hour
	maxPKIMandateValidity                  = 24 * time.Hour
	maxPKICSRBytes                         = 16 * 1024
)

const (
	pkiMandateSignatureDomain = "remote-session-runner:pki-issuance-mandate:v1\x00"
	pkiRequestSignatureDomain = "remote-session-runner:pki-certificate-request:v1\x00"
	pkiReceiptSignatureDomain = "remote-session-runner:pki-issuer-receipt:v1\x00"
)

// PKIHostPlan is a fixed role and identity tuple. Mac uses its local authority
// socket and has no public IP or public TLS listener.
type PKIHostPlan struct {
	HostID     string
	Role       PKILeafRole
	Endpoint   string
	IP         string
	URISAN     string
	CAProfile  string
	CommonName string
}

func BuildPKIHostPlan(hostID string) (PKIHostPlan, error) {
	if hostID == "mac-control-plane" {
		return PKIHostPlan{
			HostID: hostID, Role: PKIReleaseAuthorityClient, Endpoint: "local-socket",
			URISAN: releaseAuthorityURI, CAProfile: PKIReleaseTransportProfile,
			CommonName: "remote-session-runner:mac-control-plane:release-authority-client",
		}, nil
	}
	for _, route := range BuildReleaseTransportTLSPlans() {
		if route.HostID == hostID {
			return PKIHostPlan{
				HostID: route.HostID, Role: PKIReleaseTransportServer, Endpoint: route.PublicURL,
				IP: route.PublicIP, URISAN: route.ServerURISAN,
				CAProfile:  PKIReleaseTransportProfile,
				CommonName: "remote-session-runner:" + route.HostID + ":release-transport-server",
			}, nil
		}
	}
	return PKIHostPlan{}, ErrInvalidLanePKI
}

type PKIInstallerArtifactTuple struct {
	SourceCommit    string `json:"source_commit"`
	ToolchainDigest string `json:"toolchain_digest"`
	InputDigest     string `json:"input_digest"`
	BinaryDigest    string `json:"binary_digest"`
}

// PKIIssuanceContext has mutually exclusive normal-management and first-boot
// forms. Bootstrap binds digests only; it carries no artifacts or commands.
type PKIIssuanceContext struct {
	Mode                      string                    `json:"mode"`
	ManagementManifestID      string                    `json:"management_manifest_id,omitempty"`
	ManagementGeneration      uint64                    `json:"management_generation,omitempty"`
	BootstrapContractDigest   string                    `json:"bootstrap_contract_digest,omitempty"`
	BootstrapGeneration       uint64                    `json:"bootstrap_generation,omitempty"`
	Installer                 PKIInstallerArtifactTuple `json:"installer,omitempty"`
	BootstrapAttestationKeyID string                    `json:"bootstrap_attestation_key_id,omitempty"`
}

func (c PKIIssuanceContext) valid() bool {
	switch c.Mode {
	case "management":
		return c.ManagementManifestID != "" && c.ManagementGeneration > 0 &&
			c.BootstrapContractDigest == "" && c.BootstrapGeneration == 0 &&
			c.Installer == (PKIInstallerArtifactTuple{}) && c.BootstrapAttestationKeyID == ""
	case "bootstrap":
		return c.ManagementManifestID == "" && c.ManagementGeneration == 0 &&
			isSHA256Hex(c.BootstrapContractDigest) && c.BootstrapGeneration > 0 &&
			isCommit(c.Installer.SourceCommit) && isSHA256Hex(c.Installer.ToolchainDigest) &&
			isSHA256Hex(c.Installer.InputDigest) && isSHA256Hex(c.Installer.BinaryDigest) &&
			c.BootstrapAttestationKeyID != ""
	default:
		return false
	}
}

type PKIIssuanceMandate struct {
	ID                  string             `json:"id"`
	ApprovalKeyID       string             `json:"approval_key_id"`
	HostID              string             `json:"host_id"`
	Role                PKILeafRole        `json:"leaf_role"`
	Context             PKIIssuanceContext `json:"context"`
	URISAN              string             `json:"uri_san"`
	IP                  string             `json:"ip_san,omitempty"`
	CAProfile           string             `json:"ca_profile"`
	NotBefore           time.Time          `json:"not_before"`
	ExpiresAt           time.Time          `json:"expires_at"`
	CertificateNotAfter time.Time          `json:"certificate_not_after"`
	Signature           []byte             `json:"signature"`
}

type mandatePayload struct {
	ApprovalKeyID       string             `json:"approval_key_id"`
	HostID              string             `json:"host_id"`
	Role                PKILeafRole        `json:"leaf_role"`
	Context             PKIIssuanceContext `json:"context"`
	URISAN              string             `json:"uri_san"`
	IP                  string             `json:"ip_san,omitempty"`
	CAProfile           string             `json:"ca_profile"`
	NotBefore           time.Time          `json:"not_before"`
	ExpiresAt           time.Time          `json:"expires_at"`
	CertificateNotAfter time.Time          `json:"certificate_not_after"`
}

func (m PKIIssuanceMandate) payload() mandatePayload {
	return mandatePayload{m.ApprovalKeyID, m.HostID, m.Role, m.Context, m.URISAN, m.IP, m.CAProfile, m.NotBefore, m.ExpiresAt, m.CertificateNotAfter}
}

// SignPKIIssuanceMandate signs a one-use mandate using an approval identity.
// The private key remains with the caller and is never part of a PKI request.
func SignPKIIssuanceMandate(m PKIIssuanceMandate, keyID string, key ed25519.PrivateKey) (PKIIssuanceMandate, error) {
	if len(key) != ed25519.PrivateKeySize || keyID == "" {
		return PKIIssuanceMandate{}, ErrInvalidLanePKI
	}
	m.ID, m.ApprovalKeyID, m.Signature = "", keyID, nil
	data, err := json.Marshal(m.payload())
	if err != nil {
		return PKIIssuanceMandate{}, ErrInvalidLanePKI
	}
	hash := sha256.Sum256(data)
	m.ID = hex.EncodeToString(hash[:])
	m.Signature = ed25519.Sign(key, signatureMessage(pkiMandateSignatureDomain, data))
	return m, nil
}

type LocalPKIKeyStore struct {
	root string
}

// NewLocalPKIKeyStore creates a host-local key store rooted in a dedicated
// installer directory. Production callers choose the fixed installed root;
// tests use a fresh t.TempDir. Existing identities are never overwritten.
func NewLocalPKIKeyStore(root string) (*LocalPKIKeyStore, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, ErrInvalidLanePKI
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrInvalidLanePKI
	}
	return &LocalPKIKeyStore{root: root}, nil
}

type localPKIKey struct {
	hostID string
	role   PKILeafRole
	path   string
	signer crypto.Signer
}

func (s *LocalPKIKeyStore) CreateCSR(plan PKIHostPlan) (*localPKIKey, []byte, error) {
	canonical, err := BuildPKIHostPlan(plan.HostID)
	if err != nil || canonical != plan || s == nil {
		return nil, nil, ErrInvalidLanePKI
	}
	rootInfo, err := os.Lstat(s.root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || rootInfo.Mode().Perm()&0o077 != 0 {
		return nil, nil, ErrInvalidLanePKI
	}
	hostDir := filepath.Join(s.root, plan.HostID)
	if err := os.Mkdir(hostDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, nil, ErrInvalidLanePKI
	}
	info, err := os.Lstat(hostDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, nil, ErrInvalidLanePKI
	}
	keyPath := filepath.Join(hostDir, string(plan.Role)+".pk8")
	if _, err := os.Lstat(keyPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		return nil, nil, ErrInvalidLanePKI
	}
	_, signer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, ErrInvalidLanePKI
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		return nil, nil, ErrInvalidLanePKI
	}
	file, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, nil, ErrInvalidLanePKI
	}
	writeErr := pem.Encode(file, &pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(keyPath)
		return nil, nil, ErrInvalidLanePKI
	}
	if directory, err := os.Open(hostDir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: plan.CommonName},
		URIs:    []*url.URL{mustParseURI(plan.URISAN)},
		IPAddresses: func() []net.IP {
			if plan.IP == "" {
				return nil
			}
			return []net.IP{net.ParseIP(plan.IP)}
		}(),
	}, signer)
	if err != nil {
		_ = os.Remove(keyPath)
		return nil, nil, ErrInvalidLanePKI
	}
	return &localPKIKey{hostID: plan.HostID, role: plan.Role, path: keyPath, signer: signer}, csrDER, nil
}

func (s *LocalPKIKeyStore) Load(hostID string, role PKILeafRole) (*localPKIKey, error) {
	plan, err := BuildPKIHostPlan(hostID)
	if err != nil || plan.Role != role || s == nil {
		return nil, ErrInvalidLanePKI
	}
	rootInfo, err := os.Lstat(s.root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || rootInfo.Mode().Perm()&0o077 != 0 {
		return nil, ErrInvalidLanePKI
	}
	hostDir := filepath.Join(s.root, hostID)
	hostInfo, err := os.Lstat(hostDir)
	if err != nil || !hostInfo.IsDir() || hostInfo.Mode()&os.ModeSymlink != 0 || hostInfo.Mode().Perm()&0o077 != 0 {
		return nil, ErrInvalidLanePKI
	}
	path := filepath.Join(s.root, hostID, string(role)+".pk8")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidLanePKI
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrInvalidLanePKI
	}
	block, rest := pem.Decode(contents)
	if block == nil || block.Type != "PRIVATE KEY" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, ErrInvalidLanePKI
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, ErrInvalidLanePKI
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, ErrInvalidLanePKI
	}
	return &localPKIKey{hostID: hostID, role: role, path: path, signer: signer}, nil
}

func (k *localPKIKey) matches(cert *x509.Certificate) bool {
	if k == nil || k.signer == nil || cert == nil {
		return false
	}
	got, err := x509.MarshalPKIXPublicKey(k.signer.Public())
	if err != nil {
		return false
	}
	want, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	return err == nil && string(got) == string(want)
}

type PKICertificateRequest struct {
	ID                   string             `json:"id"`
	IssuanceMandateID    string             `json:"pki_issuance_mandate_id"`
	HostID               string             `json:"host_id"`
	Role                 PKILeafRole        `json:"leaf_role"`
	Context              PKIIssuanceContext `json:"context"`
	URISAN               string             `json:"uri_san"`
	IP                   string             `json:"ip_san,omitempty"`
	CSRDER               []byte             `json:"csr_der"`
	PublicKeyFingerprint string             `json:"public_key_fingerprint"`
	CAProfile            string             `json:"ca_profile"`
	CertificateNotAfter  time.Time          `json:"certificate_not_after"`
	Signature            []byte             `json:"host_signature"`
}

type pkiRequestPayload struct {
	IssuanceMandateID    string             `json:"pki_issuance_mandate_id"`
	HostID               string             `json:"host_id"`
	Role                 PKILeafRole        `json:"leaf_role"`
	Context              PKIIssuanceContext `json:"context"`
	URISAN               string             `json:"uri_san"`
	IP                   string             `json:"ip_san,omitempty"`
	CSRDER               []byte             `json:"csr_der"`
	PublicKeyFingerprint string             `json:"public_key_fingerprint"`
	CAProfile            string             `json:"ca_profile"`
	CertificateNotAfter  time.Time          `json:"certificate_not_after"`
}

func (r PKICertificateRequest) payload() pkiRequestPayload {
	return pkiRequestPayload{r.IssuanceMandateID, r.HostID, r.Role, r.Context, r.URISAN, r.IP, r.CSRDER, r.PublicKeyFingerprint, r.CAProfile, r.CertificateNotAfter}
}

type PKIIssuanceMandateSource interface {
	// Implementations resolve only an authority-authenticated opaque ID from
	// protected anchor state and return the exact approved mandate.
	LookupPKIIssuanceMandate(opaqueMandateID string) (PKIIssuanceMandate, error)
}

type PKICertificateRequestOutbox interface {
	PublishPKICertificateRequest(PKICertificateRequest) error
}

type PKIHostRequestAttestor interface {
	SignHostPKIRequest(message []byte) ([]byte, error)
}

// FixedPKIInstaller represents the constrained installer entry point. Its only
// request input is an opaque one-use mandate ID; role, endpoint, SANs, local
// key path, and outbox are fixed by construction.
type FixedPKIInstaller struct {
	plan     PKIHostPlan
	keys     *LocalPKIKeyStore
	mandates PKIIssuanceMandateSource
	attestor PKIHostRequestAttestor
	outbox   PKICertificateRequestOutbox
}

func NewFixedPKIInstaller(hostID string, keys *LocalPKIKeyStore, mandates PKIIssuanceMandateSource, attestor PKIHostRequestAttestor, outbox PKICertificateRequestOutbox) (*FixedPKIInstaller, error) {
	plan, err := BuildPKIHostPlan(hostID)
	if err != nil || keys == nil || mandates == nil || attestor == nil || outbox == nil {
		return nil, ErrInvalidLanePKI
	}
	return &FixedPKIInstaller{plan: plan, keys: keys, mandates: mandates, attestor: attestor, outbox: outbox}, nil
}

func (i *FixedPKIInstaller) RequestCertificate(opaqueMandateID string) error {
	if i == nil || !isSHA256Hex(opaqueMandateID) {
		return ErrInvalidLanePKI
	}
	mandate, err := i.mandates.LookupPKIIssuanceMandate(opaqueMandateID)
	if err != nil || mandate.ID != opaqueMandateID || mandate.HostID != i.plan.HostID || mandate.Role != i.plan.Role ||
		mandate.URISAN != i.plan.URISAN || mandate.IP != i.plan.IP || mandate.CAProfile != i.plan.CAProfile || !mandate.Context.valid() {
		return ErrInvalidLanePKI
	}
	key, csrDER, err := i.keys.CreateCSR(i.plan)
	if err != nil {
		return ErrInvalidLanePKI
	}
	request, message, err := buildPKICertificateRequest(i.plan, mandate, csrDER, key)
	if err != nil {
		return ErrInvalidLanePKI
	}
	request.Signature, err = i.attestor.SignHostPKIRequest(signatureMessage(pkiRequestSignatureDomain, message))
	if err != nil || len(request.Signature) != ed25519.SignatureSize {
		return ErrInvalidLanePKI
	}
	if err := i.outbox.PublishPKICertificateRequest(request); err != nil {
		return ErrInvalidLanePKI
	}
	return nil
}

// NewPKICertificateRequest binds a caller-held public CSR to the fixed host
// plan. The CSR must have been generated by the matching LocalPKIKeyStore
// handle; the key itself is never accepted or serialized in this record.
func NewPKICertificateRequest(plan PKIHostPlan, mandate PKIIssuanceMandate, csrDER []byte, key *localPKIKey, hostAttestationKey ed25519.PrivateKey) (PKICertificateRequest, error) {
	if len(hostAttestationKey) != ed25519.PrivateKeySize {
		return PKICertificateRequest{}, ErrInvalidLanePKI
	}
	request, payload, err := buildPKICertificateRequest(plan, mandate, csrDER, key)
	if err != nil {
		return PKICertificateRequest{}, ErrInvalidLanePKI
	}
	request.Signature = ed25519.Sign(hostAttestationKey, signatureMessage(pkiRequestSignatureDomain, payload))
	return request, nil
}

func buildPKICertificateRequest(plan PKIHostPlan, mandate PKIIssuanceMandate, csrDER []byte, key *localPKIKey) (PKICertificateRequest, []byte, error) {
	canonical, err := BuildPKIHostPlan(plan.HostID)
	if err != nil || canonical != plan || mandate.HostID != plan.HostID || mandate.Role != plan.Role ||
		mandate.URISAN != plan.URISAN || mandate.IP != plan.IP || mandate.CAProfile != plan.CAProfile ||
		!mandate.Context.valid() || key == nil || key.hostID != plan.HostID || key.role != plan.Role || len(csrDER) == 0 || len(csrDER) > maxPKICSRBytes {
		return PKICertificateRequest{}, nil, ErrInvalidLanePKI
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil || csr.CheckSignature() != nil || validatePKICSR(csr, plan) != nil {
		return PKICertificateRequest{}, nil, ErrInvalidLanePKI
	}
	keyPublic, err := x509.MarshalPKIXPublicKey(key.signer.Public())
	csrPublic, csrErr := x509.MarshalPKIXPublicKey(csr.PublicKey)
	if err != nil || csrErr != nil || string(keyPublic) != string(csrPublic) {
		return PKICertificateRequest{}, nil, ErrInvalidLanePKI
	}
	spki, err := x509.MarshalPKIXPublicKey(csr.PublicKey)
	if err != nil {
		return PKICertificateRequest{}, nil, ErrInvalidLanePKI
	}
	spkiHash := sha256.Sum256(spki)
	request := PKICertificateRequest{
		IssuanceMandateID: mandate.ID, HostID: plan.HostID, Role: plan.Role, Context: mandate.Context,
		URISAN: plan.URISAN, IP: plan.IP, CSRDER: append([]byte(nil), csrDER...),
		PublicKeyFingerprint: hex.EncodeToString(spkiHash[:]), CAProfile: plan.CAProfile,
		CertificateNotAfter: mandate.CertificateNotAfter,
	}
	payload, err := json.Marshal(request.payload())
	if err != nil {
		return PKICertificateRequest{}, nil, ErrInvalidLanePKI
	}
	id := sha256.Sum256(payload)
	request.ID = hex.EncodeToString(id[:])
	return request, payload, nil
}

func validatePKICSR(csr *x509.CertificateRequest, plan PKIHostPlan) error {
	publicKey, publicKeyOK := csr.PublicKey.(ed25519.PublicKey)
	if !publicKeyOK || len(publicKey) != ed25519.PublicKeySize || csr.Subject.CommonName != plan.CommonName || !hasOnlyCommonName(csr.Subject, plan.CommonName) || len(csr.Subject.Organization) != 0 ||
		len(csr.Subject.OrganizationalUnit) != 0 || len(csr.Subject.Country) != 0 ||
		len(csr.Subject.Province) != 0 || len(csr.Subject.Locality) != 0 ||
		len(csr.DNSNames) != 0 || len(csr.EmailAddresses) != 0 || len(csr.URIs) != 1 || csr.URIs[0].String() != plan.URISAN || len(csr.ExtraExtensions) != 0 {
		return ErrInvalidLanePKI
	}
	for _, extension := range csr.Extensions {
		if !extension.Id.Equal(asn1.ObjectIdentifier{2, 5, 29, 17}) {
			return ErrInvalidLanePKI
		}
	}
	if len(csr.Extensions) > 1 {
		return ErrInvalidLanePKI
	}
	if plan.IP == "" {
		if len(csr.IPAddresses) != 0 {
			return ErrInvalidLanePKI
		}
	} else if len(csr.IPAddresses) != 1 || !csr.IPAddresses[0].Equal(net.ParseIP(plan.IP)) {
		return ErrInvalidLanePKI
	}
	return nil
}

type PKIIssuanceJournal interface {
	Consume(mandateID, requestID string) error
}

type MemoryPKIIssuanceJournal struct {
	mu       sync.Mutex
	consumed map[string]string
}

func NewMemoryPKIIssuanceJournal() *MemoryPKIIssuanceJournal {
	return &MemoryPKIIssuanceJournal{consumed: make(map[string]string)}
}

func (j *MemoryPKIIssuanceJournal) Consume(mandateID, requestID string) error {
	if j == nil || mandateID == "" || requestID == "" {
		return ErrInvalidLanePKI
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, exists := j.consumed[mandateID]; exists {
		return ErrInvalidLanePKI
	}
	j.consumed[mandateID] = requestID
	return nil
}

type PKICertificatePackage struct {
	HostID            string      `json:"host_id"`
	Role              PKILeafRole `json:"leaf_role"`
	RequestID         string      `json:"request_id"`
	CertificateDER    []byte      `json:"certificate_der"`
	TrustRootsDER     [][]byte    `json:"trust_roots_der"`
	CertificateSHA256 string      `json:"certificate_sha256"`
	SPKISHA256        string      `json:"spki_sha256"`
}

type PKIIssuerReceipt struct {
	ID                string      `json:"id"`
	IssuerKeyID       string      `json:"issuer_key_id"`
	MandateID         string      `json:"mandate_id"`
	RequestID         string      `json:"request_id"`
	HostID            string      `json:"host_id"`
	Role              PKILeafRole `json:"leaf_role"`
	PackageDigest     string      `json:"package_digest"`
	IssuerFingerprint string      `json:"issuer_fingerprint"`
	IssuedAt          time.Time   `json:"issued_at"`
	Signature         []byte      `json:"signature"`
}

type receiptPayload struct {
	IssuerKeyID       string      `json:"issuer_key_id"`
	MandateID         string      `json:"mandate_id"`
	RequestID         string      `json:"request_id"`
	HostID            string      `json:"host_id"`
	Role              PKILeafRole `json:"leaf_role"`
	PackageDigest     string      `json:"package_digest"`
	IssuerFingerprint string      `json:"issuer_fingerprint"`
	IssuedAt          time.Time   `json:"issued_at"`
}

func (r PKIIssuerReceipt) payload() receiptPayload {
	return receiptPayload{r.IssuerKeyID, r.MandateID, r.RequestID, r.HostID, r.Role, r.PackageDigest, r.IssuerFingerprint, r.IssuedAt}
}

type PKIIssuer struct {
	mu               sync.Mutex
	caCertificate    *x509.Certificate
	caSigner         crypto.Signer
	issuerKeyID      string
	receiptKey       ed25519.PrivateKey
	approvalKeys     map[string]ed25519.PublicKey
	hostKeys         map[string]ed25519.PublicKey
	journal          PKIIssuanceJournal
	now              func() time.Time
	bootstrapHealthy map[string]bool
	bootstrapIssued  map[string]bool
}

func NewPKIIssuer(caCertificate *x509.Certificate, caSigner crypto.Signer, issuerKeyID string, receiptKey ed25519.PrivateKey, approvalKeys, hostKeys map[string]ed25519.PublicKey, journal PKIIssuanceJournal, now func() time.Time) (*PKIIssuer, error) {
	if caCertificate == nil || caSigner == nil || issuerKeyID == "" || len(receiptKey) != ed25519.PrivateKeySize || journal == nil ||
		!caCertificate.IsCA || !caCertificate.BasicConstraintsValid || caCertificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, ErrInvalidLanePKI
	}
	certPublic, err := x509.MarshalPKIXPublicKey(caCertificate.PublicKey)
	keyPublic, keyErr := x509.MarshalPKIXPublicKey(caSigner.Public())
	if err != nil || keyErr != nil || string(certPublic) != string(keyPublic) {
		return nil, ErrInvalidLanePKI
	}
	if now == nil {
		now = time.Now
	}
	return &PKIIssuer{
		caCertificate: caCertificate, caSigner: caSigner, issuerKeyID: issuerKeyID,
		receiptKey:   append(ed25519.PrivateKey(nil), receiptKey...),
		approvalKeys: cloneEd25519Keys(approvalKeys), hostKeys: cloneEd25519Keys(hostKeys),
		journal: journal, now: now, bootstrapHealthy: make(map[string]bool), bootstrapIssued: make(map[string]bool),
	}, nil
}

// RecordFirstHealthGate permanently closes the bootstrap issuance exception
// for this host. A durable host-anchor implementation is supplied with L3.
func (i *PKIIssuer) RecordFirstHealthGate(hostID string) error {
	if _, err := BuildPKIHostPlan(hostID); err != nil || hostID == "mac-control-plane" || i == nil {
		return ErrInvalidLanePKI
	}
	i.mu.Lock()
	i.bootstrapHealthy[hostID] = true
	i.mu.Unlock()
	return nil
}

func (i *PKIIssuer) Issue(mandate PKIIssuanceMandate, request PKICertificateRequest) (PKICertificatePackage, PKIIssuerReceipt, error) {
	if i == nil || mandate.ID == "" || request.ID == "" || request.IssuanceMandateID != mandate.ID ||
		request.HostID != mandate.HostID || request.Role != mandate.Role || request.Context != mandate.Context ||
		request.URISAN != mandate.URISAN || request.IP != mandate.IP || request.CAProfile != mandate.CAProfile ||
		request.CertificateNotAfter != mandate.CertificateNotAfter || !request.Context.valid() {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	plan, err := BuildPKIHostPlan(request.HostID)
	if err != nil || request.Role != plan.Role || request.URISAN != plan.URISAN || request.IP != plan.IP || request.CAProfile != plan.CAProfile {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	approvalKey := i.approvalKeys[mandate.ApprovalKeyID]
	mandateBytes, err := json.Marshal(mandate.payload())
	if err != nil || len(approvalKey) != ed25519.PublicKeySize || !ed25519.Verify(approvalKey, signatureMessage(pkiMandateSignatureDomain, mandateBytes), mandate.Signature) {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	mandateHash := sha256.Sum256(mandateBytes)
	if mandate.ID != hex.EncodeToString(mandateHash[:]) || !mandate.Context.valid() {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	now := i.now().UTC()
	if now.Before(i.caCertificate.NotBefore) || !now.Before(i.caCertificate.NotAfter) ||
		now.Before(mandate.NotBefore) || !now.Before(mandate.ExpiresAt) || !mandate.CertificateNotAfter.After(now) ||
		mandate.CertificateNotAfter.Sub(now) > maxPKILeafValidity ||
		mandate.ExpiresAt.Sub(mandate.NotBefore) > maxPKIMandateValidity || mandate.CertificateNotAfter.After(i.caCertificate.NotAfter) ||
		len(request.CSRDER) == 0 || len(request.CSRDER) > maxPKICSRBytes {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	csr, err := x509.ParseCertificateRequest(request.CSRDER)
	if err != nil || csr.CheckSignature() != nil || validatePKICSR(csr, plan) != nil {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	spki, err := x509.MarshalPKIXPublicKey(csr.PublicKey)
	if err != nil || fingerprintBytes(spki) != request.PublicKeyFingerprint {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	hostKey := i.hostKeys[request.HostID]
	requestBytes, err := json.Marshal(request.payload())
	if err != nil || len(hostKey) != ed25519.PublicKeySize || !ed25519.Verify(hostKey, signatureMessage(pkiRequestSignatureDomain, requestBytes), request.Signature) {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	requestHash := sha256.Sum256(requestBytes)
	if request.ID != hex.EncodeToString(requestHash[:]) {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if mandate.Context.Mode == "bootstrap" && (i.bootstrapHealthy[mandate.HostID] || i.bootstrapIssued[mandate.HostID]) {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	if err := i.journal.Consume(mandate.ID, request.ID); err != nil {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	if mandate.Context.Mode == "bootstrap" {
		i.bootstrapIssued[mandate.HostID] = true
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	dnsName := ""
	if plan.IP != "" {
		dnsName = ""
	}
	usage := x509.ExtKeyUsageClientAuth
	if plan.Role == PKIReleaseTransportServer {
		usage = x509.ExtKeyUsageServerAuth
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: plan.CommonName},
		NotBefore: now.Add(-time.Minute), NotAfter: mandate.CertificateNotAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
		URIs: []*url.URL{mustParseURI(plan.URISAN)},
		IPAddresses: func() []net.IP {
			if plan.IP == "" {
				return nil
			}
			return []net.IP{net.ParseIP(plan.IP)}
		}(),
		DNSNames: func() []string {
			if dnsName == "" {
				return nil
			}
			return []string{dnsName}
		}(),
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, i.caCertificate, csr.PublicKey, i.caSigner)
	if err != nil {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	leaf, err := x509.ParseCertificate(certificateDER)
	if err != nil {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	caDER := append([]byte(nil), i.caCertificate.Raw...)
	pkg := PKICertificatePackage{
		HostID: request.HostID, Role: request.Role, RequestID: request.ID,
		CertificateDER: certificateDER, TrustRootsDER: [][]byte{caDER},
		CertificateSHA256: fingerprintBytes(certificateDER),
		SPKISHA256:        fingerprintPublicKey(leaf.PublicKey),
	}
	packageDigest, err := PKICertificatePackageDigest(pkg)
	if err != nil {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	issuerFingerprint := fingerprintBytes(i.caCertificate.Raw)
	receipt := PKIIssuerReceipt{
		IssuerKeyID: i.issuerKeyID, MandateID: mandate.ID, RequestID: request.ID,
		HostID: request.HostID, Role: request.Role, PackageDigest: packageDigest,
		IssuerFingerprint: issuerFingerprint, IssuedAt: now,
	}
	receiptBytes, err := json.Marshal(receipt.payload())
	if err != nil {
		return PKICertificatePackage{}, PKIIssuerReceipt{}, ErrInvalidLanePKI
	}
	receiptHash := sha256.Sum256(receiptBytes)
	receipt.ID = hex.EncodeToString(receiptHash[:])
	receipt.Signature = ed25519.Sign(i.receiptKey, signatureMessage(pkiReceiptSignatureDomain, receiptBytes))
	return pkg, receipt, nil
}

func VerifyPKIIssuerReceipt(receipt PKIIssuerReceipt, key ed25519.PublicKey, pkg PKICertificatePackage) error {
	if len(key) != ed25519.PublicKeySize || receipt.ID == "" || receipt.HostID != pkg.HostID || receipt.Role != pkg.Role ||
		receipt.RequestID != pkg.RequestID || receipt.PackageDigest != mustPackageDigest(pkg) {
		return ErrInvalidLanePKI
	}
	data, err := json.Marshal(receipt.payload())
	if err != nil || !ed25519.Verify(key, signatureMessage(pkiReceiptSignatureDomain, data), receipt.Signature) {
		return ErrInvalidLanePKI
	}
	hash := sha256.Sum256(data)
	if receipt.ID != hex.EncodeToString(hash[:]) {
		return ErrInvalidLanePKI
	}
	issuerTrusted := false
	for _, rootDER := range pkg.TrustRootsDER {
		root, parseErr := x509.ParseCertificate(rootDER)
		if parseErr == nil && root.IsCA && fingerprintBytes(root.Raw) == receipt.IssuerFingerprint {
			issuerTrusted = true
		}
	}
	if !issuerTrusted {
		return ErrInvalidLanePKI
	}
	return nil
}

func PKICertificatePackageDigest(pkg PKICertificatePackage) (string, error) {
	if pkg.HostID == "" || pkg.RequestID == "" || len(pkg.CertificateDER) == 0 || len(pkg.TrustRootsDER) == 0 {
		return "", ErrInvalidLanePKI
	}
	data, err := json.Marshal(pkg)
	if err != nil {
		return "", ErrInvalidLanePKI
	}
	return fingerprintBytes(data), nil
}

func mustPackageDigest(pkg PKICertificatePackage) string {
	digest, _ := PKICertificatePackageDigest(pkg)
	return digest
}

type PKIActivePeer struct {
	HostID            string      `json:"host_id"`
	Role              PKILeafRole `json:"leaf_role"`
	Endpoint          string      `json:"endpoint"`
	URISAN            string      `json:"uri_san"`
	IP                string      `json:"ip_san,omitempty"`
	CertificateSHA256 string      `json:"certificate_sha256"`
	SPKISHA256        string      `json:"spki_sha256"`
	OverlapNotAfter   time.Time   `json:"overlap_not_after,omitempty"`
}

type PKIActivePeerSet struct {
	Endpoint string          `json:"endpoint"`
	Active   PKIActivePeer   `json:"active"`
	Overlap  []PKIActivePeer `json:"overlap,omitempty"`
}

func ActivePeerFromPackage(endpoint string, plan PKIHostPlan, pkg PKICertificatePackage) (PKIActivePeer, error) {
	if endpoint == "" || plan.HostID != pkg.HostID || plan.Role != pkg.Role || !isSHA256Hex(pkg.CertificateSHA256) || !isSHA256Hex(pkg.SPKISHA256) {
		return PKIActivePeer{}, ErrInvalidLanePKI
	}
	return PKIActivePeer{HostID: plan.HostID, Role: plan.Role, Endpoint: endpoint, URISAN: plan.URISAN, IP: plan.IP,
		CertificateSHA256: pkg.CertificateSHA256, SPKISHA256: pkg.SPKISHA256}, nil
}

func PKIActivePeerSetDigest(set PKIActivePeerSet) (string, error) {
	if set.Endpoint == "" || !validActivePeerShape(set.Active, set.Endpoint) || len(set.Overlap) > 1 {
		return "", ErrInvalidLanePKI
	}
	for _, overlap := range set.Overlap {
		if !validActivePeerShape(overlap, set.Endpoint) || overlap.HostID != set.Active.HostID || overlap.Role != set.Active.Role ||
			overlap.URISAN != set.Active.URISAN || overlap.IP != set.Active.IP || overlap.CertificateSHA256 == set.Active.CertificateSHA256 ||
			overlap.SPKISHA256 == set.Active.SPKISHA256 || overlap.OverlapNotAfter.IsZero() {
			return "", ErrInvalidLanePKI
		}
	}
	data, err := json.Marshal(set)
	if err != nil {
		return "", ErrInvalidLanePKI
	}
	return fingerprintBytes(data), nil
}

func validateActivePeerCertificate(cert *x509.Certificate, set PKIActivePeerSet, now time.Time) error {
	if cert == nil || set.Endpoint == "" || set.Active.Endpoint != set.Endpoint || len(set.Overlap) > 1 {
		return ErrInvalidLanePKI
	}
	entries := append([]PKIActivePeer{set.Active}, set.Overlap...)
	for index, peer := range entries {
		if peer.Endpoint != set.Endpoint || peer.HostID == "" || peer.URISAN == "" ||
			!isSHA256Hex(peer.CertificateSHA256) || !isSHA256Hex(peer.SPKISHA256) ||
			(index > 0 && (peer.OverlapNotAfter.IsZero() || !now.Before(peer.OverlapNotAfter))) {
			continue
		}
		uri, err := url.Parse(peer.URISAN)
		if err != nil || len(cert.URIs) != 1 || cert.URIs[0].String() != uri.String() {
			continue
		}
		if peer.IP == "" {
			if len(cert.IPAddresses) != 0 {
				continue
			}
		} else if len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP(peer.IP)) {
			continue
		}
		if fingerprintBytes(cert.Raw) == peer.CertificateSHA256 && fingerprintPublicKey(cert.PublicKey) == peer.SPKISHA256 {
			return nil
		}
	}
	return ErrInvalidLanePKI
}

func validActivePeerShape(peer PKIActivePeer, endpoint string) bool {
	return peer.Endpoint == endpoint && peer.HostID != "" && peer.URISAN != "" &&
		isSHA256Hex(peer.CertificateSHA256) && isSHA256Hex(peer.SPKISHA256)
}

func validatePKIActivePeerSet(set PKIActivePeerSet, expected PKIHostPlan, endpoint string, now time.Time) error {
	if set.Endpoint != endpoint || set.Active.HostID != expected.HostID || set.Active.Role != expected.Role ||
		set.Active.URISAN != expected.URISAN || set.Active.IP != expected.IP ||
		!validActivePeerShape(set.Active, endpoint) || len(set.Overlap) > 1 {
		return ErrInvalidLanePKI
	}
	for _, overlap := range set.Overlap {
		if !validActivePeerShape(overlap, endpoint) || overlap.HostID != expected.HostID || overlap.Role != expected.Role ||
			overlap.URISAN != expected.URISAN || overlap.IP != expected.IP ||
			overlap.CertificateSHA256 == set.Active.CertificateSHA256 || overlap.SPKISHA256 == set.Active.SPKISHA256 ||
			!overlap.OverlapNotAfter.After(now) || overlap.OverlapNotAfter.Sub(now) > maxPKIOverlapValidity {
			return ErrInvalidLanePKI
		}
	}
	return nil
}

func BuildReleaseTransportPKITLSClientConfig(route ReleaseTransportTLSPlan, clientPackage PKICertificatePackage, clientKey *localPKIKey, rootsDER [][]byte, activePeers PKIActivePeerSet, now time.Time) (*tls.Config, error) {
	canonical := false
	for _, candidate := range BuildReleaseTransportTLSPlans() {
		canonical = canonical || candidate == route
	}
	serverPlan, planErr := BuildPKIHostPlan(route.HostID)
	if !canonical || planErr != nil || validatePKIActivePeerSet(activePeers, serverPlan, route.PublicURL, now) != nil || clientKey == nil || clientKey.hostID != "mac-control-plane" ||
		clientKey.role != PKIReleaseAuthorityClient || clientPackage.HostID != "mac-control-plane" ||
		clientPackage.Role != PKIReleaseAuthorityClient {
		return nil, ErrInvalidLanePKI
	}
	clientLeaf, err := x509.ParseCertificate(clientPackage.CertificateDER)
	if err != nil || !clientKey.matches(clientLeaf) || validatePKICertificate(clientLeaf, PKIHostPlan{
		HostID: "mac-control-plane", Role: PKIReleaseAuthorityClient, URISAN: releaseAuthorityURI,
		CAProfile: PKIReleaseTransportProfile, CommonName: "remote-session-runner:mac-control-plane:release-authority-client",
	}) != nil {
		return nil, ErrInvalidLanePKI
	}
	clientRoots, err := parsePKIRoots(clientPackage.TrustRootsDER)
	if err != nil {
		return nil, ErrInvalidLanePKI
	}
	if !samePKIRootSet(clientPackage.TrustRootsDER, rootsDER) {
		return nil, ErrInvalidLanePKI
	}
	if _, err := clientLeaf.Verify(x509.VerifyOptions{Roots: clientRoots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, ErrInvalidLanePKI
	}
	rootPool, err := parsePKIRoots(rootsDER)
	if err != nil {
		return nil, ErrInvalidLanePKI
	}
	leaf := tls.Certificate{Certificate: [][]byte{clientLeaf.Raw}, PrivateKey: clientKey.signer, Leaf: clientLeaf}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, ServerName: route.PublicIP, RootCAs: rootPool, Certificates: []tls.Certificate{leaf},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
				return ErrInvalidLanePKI
			}
			return validateActivePeerCertificate(state.PeerCertificates[0], activePeers, time.Now().UTC())
		},
	}, nil
}

func BuildReleaseTransportPKITLSServerConfig(route ReleaseTransportTLSPlan, serverPackage PKICertificatePackage, serverKey *localPKIKey, clientRootsDER [][]byte, activePeers PKIActivePeerSet, now time.Time) (*tls.Config, error) {
	canonical := false
	for _, candidate := range BuildReleaseTransportTLSPlans() {
		canonical = canonical || candidate == route
	}
	plan, err := BuildPKIHostPlan(route.HostID)
	macPlan, macErr := BuildPKIHostPlan("mac-control-plane")
	if err != nil || macErr != nil || !canonical || validatePKIActivePeerSet(activePeers, macPlan, route.PublicURL, now) != nil || serverKey == nil || serverKey.hostID != route.HostID ||
		serverKey.role != PKIReleaseTransportServer || serverPackage.HostID != route.HostID ||
		serverPackage.Role != PKIReleaseTransportServer || activePeers.Endpoint != route.PublicURL {
		return nil, ErrInvalidLanePKI
	}
	serverLeaf, err := x509.ParseCertificate(serverPackage.CertificateDER)
	if err != nil || !serverKey.matches(serverLeaf) || validatePKICertificate(serverLeaf, plan) != nil {
		return nil, ErrInvalidLanePKI
	}
	serverRoots, err := parsePKIRoots(serverPackage.TrustRootsDER)
	if err != nil {
		return nil, ErrInvalidLanePKI
	}
	if !samePKIRootSet(serverPackage.TrustRootsDER, clientRootsDER) {
		return nil, ErrInvalidLanePKI
	}
	if _, err := serverLeaf.Verify(x509.VerifyOptions{Roots: serverRoots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return nil, ErrInvalidLanePKI
	}
	clientRoots, err := parsePKIRoots(clientRootsDER)
	if err != nil {
		return nil, ErrInvalidLanePKI
	}
	leaf := tls.Certificate{Certificate: [][]byte{serverLeaf.Raw}, PrivateKey: serverKey.signer, Leaf: serverLeaf}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientRoots, Certificates: []tls.Certificate{leaf},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
				return ErrInvalidLanePKI
			}
			return validateActivePeerCertificate(state.PeerCertificates[0], activePeers, time.Now().UTC())
		},
	}, nil
}

func validatePKICertificate(cert *x509.Certificate, plan PKIHostPlan) error {
	if cert == nil || cert.Subject.CommonName != plan.CommonName || !hasOnlyCommonName(cert.Subject, plan.CommonName) || len(cert.URIs) != 1 || cert.URIs[0].String() != plan.URISAN ||
		len(cert.DNSNames) != 0 {
		return ErrInvalidLanePKI
	}
	if plan.IP == "" {
		if len(cert.IPAddresses) != 0 {
			return ErrInvalidLanePKI
		}
	} else if len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP(plan.IP)) {
		return ErrInvalidLanePKI
	}
	usage := x509.ExtKeyUsageClientAuth
	if plan.Role == PKIReleaseTransportServer {
		usage = x509.ExtKeyUsageServerAuth
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != usage || cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return ErrInvalidLanePKI
	}
	return nil
}

func hasOnlyCommonName(subject pkix.Name, commonName string) bool {
	return len(subject.Names) == 1 && subject.Names[0].Type.Equal(asn1.ObjectIdentifier{2, 5, 4, 3}) &&
		subject.Names[0].Value == commonName && len(subject.ExtraNames) == 0
}

func parsePKIRoots(der [][]byte) (*x509.CertPool, error) {
	if len(der) == 0 || len(der) > 2 {
		return nil, ErrInvalidLanePKI
	}
	pool := x509.NewCertPool()
	for _, item := range der {
		cert, err := x509.ParseCertificate(item)
		if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || !pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: item})) {
			return nil, ErrInvalidLanePKI
		}
	}
	return pool, nil
}

func samePKIRootSet(left, right [][]byte) bool {
	if len(left) == 0 || len(left) != len(right) {
		return false
	}
	leftSet := make(map[string]struct{}, len(left))
	for _, der := range left {
		certificate, err := x509.ParseCertificate(der)
		if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid {
			return false
		}
		leftSet[fingerprintBytes(certificate.Raw)] = struct{}{}
	}
	for _, der := range right {
		certificate, err := x509.ParseCertificate(der)
		if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid {
			return false
		}
		if _, exists := leftSet[fingerprintBytes(certificate.Raw)]; !exists {
			return false
		}
		delete(leftSet, fingerprintBytes(certificate.Raw))
	}
	return len(leftSet) == 0
}

type PKIInstallAuthorization struct {
	HostID               string      `json:"host_id"`
	Role                 PKILeafRole `json:"leaf_role"`
	ManagementManifestID string      `json:"management_manifest_id"`
	ManagementGeneration uint64      `json:"management_generation"`
	RequestID            string      `json:"pki_certificate_request_id"`
	IssuerReceiptID      string      `json:"issuer_receipt_id"`
	PackageDigest        string      `json:"package_digest"`
	ActivePeerSetDigest  string      `json:"active_peer_set_digest"`
}

// ValidateAuthorizedPKIPackage checks the exact package/receipt/request/local-key
// binding plus an active-peer-set digest. It is a structural L2 check; L3
// supplies canonical signed host-management authorization enforcement.
func ValidateAuthorizedPKIPackage(plan PKIHostPlan, request PKICertificateRequest, pkg PKICertificatePackage, receipt PKIIssuerReceipt, receiptKey ed25519.PublicKey, authorization PKIInstallAuthorization, activePeers PKIActivePeerSet, localKey *localPKIKey, roots []*x509.Certificate, now time.Time) error {
	if request.HostID != plan.HostID || request.Role != plan.Role || pkg.HostID != plan.HostID || pkg.Role != plan.Role ||
		pkg.RequestID != request.ID || authorization.HostID != plan.HostID || authorization.Role != plan.Role ||
		authorization.ManagementManifestID == "" || authorization.ManagementGeneration == 0 ||
		request.Context.Mode != "management" || request.Context.ManagementManifestID != authorization.ManagementManifestID ||
		request.Context.ManagementGeneration != authorization.ManagementGeneration ||
		authorization.RequestID != request.ID || authorization.IssuerReceiptID != receipt.ID ||
		authorization.PackageDigest != mustPackageDigest(pkg) || !isSHA256Hex(authorization.ActivePeerSetDigest) ||
		localKey == nil || localKey.hostID != plan.HostID || localKey.role != plan.Role {
		return ErrInvalidLanePKI
	}
	activePeerDigest, activePeerErr := PKIActivePeerSetDigest(activePeers)
	if activePeerErr != nil || activePeerDigest != authorization.ActivePeerSetDigest ||
		validateInstallActivePeerSet(plan, activePeers, now) != nil ||
		VerifyPKIIssuerReceipt(receipt, receiptKey, pkg) != nil || VerifyCertificatePackage(pkg, request, plan, roots, now) != nil {
		return ErrInvalidLanePKI
	}
	leaf, err := x509.ParseCertificate(pkg.CertificateDER)
	if err != nil || !localKey.matches(leaf) || validatePKICertificate(leaf, plan) != nil {
		return ErrInvalidLanePKI
	}
	if len(roots) == 0 {
		return ErrInvalidLanePKI
	}
	pool := x509.NewCertPool()
	for _, root := range roots {
		if root == nil || !root.IsCA || !root.BasicConstraintsValid {
			return ErrInvalidLanePKI
		}
		pool.AddCert(root)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: pool, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{func() x509.ExtKeyUsage {
			if plan.Role == PKIReleaseTransportServer {
				return x509.ExtKeyUsageServerAuth
			}
			return x509.ExtKeyUsageClientAuth
		}()},
	}); err != nil {
		return ErrInvalidLanePKI
	}
	return nil
}

func validateInstallActivePeerSet(plan PKIHostPlan, set PKIActivePeerSet, now time.Time) error {
	if plan.Role == PKIReleaseTransportServer {
		for _, route := range BuildReleaseTransportTLSPlans() {
			if route.HostID == plan.HostID {
				peer, err := BuildPKIHostPlan("mac-control-plane")
				if err != nil {
					return ErrInvalidLanePKI
				}
				return validatePKIActivePeerSet(set, peer, route.PublicURL, now)
			}
		}
		return ErrInvalidLanePKI
	}
	for _, route := range BuildReleaseTransportTLSPlans() {
		if route.PublicURL == set.Endpoint {
			peer, err := BuildPKIHostPlan(route.HostID)
			if err != nil {
				return ErrInvalidLanePKI
			}
			return validatePKIActivePeerSet(set, peer, route.PublicURL, now)
		}
	}
	return ErrInvalidLanePKI
}

type PKIBootstrapHandoff struct {
	HostID                    string                    `json:"host_id"`
	BootstrapContractDigest   string                    `json:"bootstrap_contract_digest"`
	BootstrapGeneration       uint64                    `json:"bootstrap_generation"`
	Installer                 PKIInstallerArtifactTuple `json:"installer_tuple"`
	BootstrapAttestationKeyID string                    `json:"bootstrap_attestation_key_id"`
	Request                   PKICertificateRequest     `json:"request"`
	Package                   PKICertificatePackage     `json:"certificate_trust_package"`
	Receipt                   PKIIssuerReceipt          `json:"issuer_receipt"`
}

type PKIBootstrapGate struct {
	mu      sync.Mutex
	healthy map[string]bool
	used    map[string]bool
}

func NewPKIBootstrapGate() *PKIBootstrapGate {
	return &PKIBootstrapGate{healthy: make(map[string]bool), used: make(map[string]bool)}
}

func (g *PKIBootstrapGate) MarkHealthy(hostID string) error {
	if _, err := BuildPKIHostPlan(hostID); err != nil || hostID == "mac-control-plane" || g == nil {
		return ErrInvalidLanePKI
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.healthy[hostID] = true
	return nil
}

// AcceptBootstrapHandoff models the one-time root-console public-only handoff.
// The handoff type has no fields for private keys, source, artifacts, commands,
// or reusable credentials. L3 supplies durable anchor enforcement.
func (g *PKIBootstrapGate) AcceptBootstrapHandoff(handoff PKIBootstrapHandoff, receiptKey, hostAttestationKey ed25519.PublicKey, now time.Time) error {
	plan, err := BuildPKIHostPlan(handoff.HostID)
	if err != nil || handoff.HostID == "mac-control-plane" || g == nil ||
		handoff.Request.Context.Mode != "bootstrap" ||
		handoff.Request.Context.BootstrapContractDigest != handoff.BootstrapContractDigest ||
		handoff.Request.Context.BootstrapGeneration != handoff.BootstrapGeneration ||
		handoff.Request.Context.Installer != handoff.Installer ||
		handoff.Request.Context.BootstrapAttestationKeyID != handoff.BootstrapAttestationKeyID || !handoff.Request.Context.valid() ||
		handoff.Request.HostID != handoff.HostID || handoff.Request.Role != plan.Role ||
		handoff.Package.HostID != handoff.HostID || handoff.Package.Role != plan.Role ||
		handoff.Package.RequestID != handoff.Request.ID {
		return ErrInvalidLanePKI
	}
	requestBytes, err := json.Marshal(handoff.Request.payload())
	if err != nil || len(hostAttestationKey) != ed25519.PublicKeySize || !ed25519.Verify(hostAttestationKey, signatureMessage(pkiRequestSignatureDomain, requestBytes), handoff.Request.Signature) {
		return ErrInvalidLanePKI
	}
	requestHash := sha256.Sum256(requestBytes)
	if handoff.Request.ID != hex.EncodeToString(requestHash[:]) || VerifyPKIIssuerReceipt(handoff.Receipt, receiptKey, handoff.Package) != nil ||
		handoff.Receipt.MandateID != handoff.Request.IssuanceMandateID || VerifyCertificatePackage(handoff.Package, handoff.Request, plan, func() []*x509.Certificate {
		roots := make([]*x509.Certificate, 0, len(handoff.Package.TrustRootsDER))
		for _, der := range handoff.Package.TrustRootsDER {
			root, parseErr := x509.ParseCertificate(der)
			if parseErr != nil {
				return nil
			}
			roots = append(roots, root)
		}
		return roots
	}(), now) != nil {
		return ErrInvalidLanePKI
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.healthy[handoff.HostID] || g.used[handoff.HostID] {
		return ErrInvalidLanePKI
	}
	g.used[handoff.HostID] = true
	return nil
}

func VerifyCertificatePackage(pkg PKICertificatePackage, request PKICertificateRequest, plan PKIHostPlan, roots []*x509.Certificate, now time.Time) error {
	if pkg.HostID != request.HostID || pkg.Role != request.Role || pkg.RequestID != request.ID ||
		fingerprintBytes(pkg.CertificateDER) != pkg.CertificateSHA256 || !isSHA256Hex(pkg.SPKISHA256) {
		return ErrInvalidLanePKI
	}
	leaf, err := x509.ParseCertificate(pkg.CertificateDER)
	if err != nil || validatePKICertificate(leaf, plan) != nil || fingerprintPublicKey(leaf.PublicKey) != pkg.SPKISHA256 {
		return ErrInvalidLanePKI
	}
	rootDER := make([][]byte, 0, len(roots))
	for _, root := range roots {
		if root == nil {
			return ErrInvalidLanePKI
		}
		rootDER = append(rootDER, root.Raw)
	}
	if !samePKIRootSet(pkg.TrustRootsDER, rootDER) {
		return ErrInvalidLanePKI
	}
	csr, err := x509.ParseCertificateRequest(request.CSRDER)
	if err != nil || csr.CheckSignature() != nil || validatePKICSR(csr, plan) != nil {
		return ErrInvalidLanePKI
	}
	csrKey, err := x509.MarshalPKIXPublicKey(csr.PublicKey)
	certKey, certErr := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil || certErr != nil || string(csrKey) != string(certKey) || len(roots) == 0 {
		return ErrInvalidLanePKI
	}
	pool := x509.NewCertPool()
	for _, root := range roots {
		if root == nil || !root.IsCA || !root.BasicConstraintsValid {
			return ErrInvalidLanePKI
		}
		pool.AddCert(root)
	}
	usage := x509.ExtKeyUsageClientAuth
	if plan.Role == PKIReleaseTransportServer {
		usage = x509.ExtKeyUsageServerAuth
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
		return ErrInvalidLanePKI
	}
	return nil
}

func cloneEd25519Keys(source map[string]ed25519.PublicKey) map[string]ed25519.PublicKey {
	copyOf := make(map[string]ed25519.PublicKey, len(source))
	for key, value := range source {
		copyOf[key] = append(ed25519.PublicKey(nil), value...)
	}
	return copyOf
}

func fingerprintBytes(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

func signatureMessage(domain string, payload []byte) []byte {
	message := make([]byte, 0, len(domain)+len(payload))
	message = append(message, domain...)
	message = append(message, payload...)
	return message
}

func fingerprintPublicKey(publicKey any) string {
	encoded, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return ""
	}
	return fingerprintBytes(encoded)
}

func isSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func isCommit(value string) bool {
	if len(value) != 40 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func mustParseURI(raw string) *url.URL {
	uri, err := url.Parse(raw)
	if err != nil {
		panic(fmt.Sprintf("invalid fixed lane URI: %s", raw))
	}
	return uri
}
