package laneinstall

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

type lanesL204Harness struct {
	now          time.Time
	ca           *x509.Certificate
	caSigner     *ecdsa.PrivateKey
	approvalPub  ed25519.PublicKey
	approvalPriv ed25519.PrivateKey
	receiptPub   ed25519.PublicKey
	receiptPriv  ed25519.PrivateKey
	hostPublic   map[string]ed25519.PublicKey
	hostPrivate  map[string]ed25519.PrivateKey
	issuer       *PKIIssuer
	journal      *MemoryPKIIssuanceJournal
	mandateSeq   int
}

type lanesL204Issued struct {
	plan    PKIHostPlan
	store   *LocalPKIKeyStore
	key     *localPKIKey
	csr     []byte
	mandate PKIIssuanceMandate
	request PKICertificateRequest
	pkg     PKICertificatePackage
	receipt PKIIssuerReceipt
}

type lanesL204MandateSource struct{ mandates map[string]PKIIssuanceMandate }

func (s lanesL204MandateSource) LookupPKIIssuanceMandate(id string) (PKIIssuanceMandate, error) {
	mandate, ok := s.mandates[id]
	if !ok {
		return PKIIssuanceMandate{}, ErrInvalidLanePKI
	}
	return mandate, nil
}

type lanesL204Outbox struct{ requests []PKICertificateRequest }

func (o *lanesL204Outbox) PublishPKICertificateRequest(request PKICertificateRequest) error {
	o.requests = append(o.requests, request)
	return nil
}

type lanesL204HostAttestor struct{ key ed25519.PrivateKey }

func (a lanesL204HostAttestor) SignHostPKIRequest(message []byte) ([]byte, error) {
	return ed25519.Sign(a.key, message), nil
}

func newLanesL204Harness(t *testing.T) *lanesL204Harness {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	caSigner, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: serial, Subject: pkixName("lane PKI fixture root"),
		NotBefore: now.Add(-24 * time.Hour), NotAfter: now.AddDate(2, 0, 0),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caSigner.Public(), caSigner)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	approvalPub, approvalPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	receiptPub, receiptPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostPublic := make(map[string]ed25519.PublicKey)
	hostPrivate := make(map[string]ed25519.PrivateKey)
	for _, host := range []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"} {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		hostPublic[host], hostPrivate[host] = public, private
	}
	journal := NewMemoryPKIIssuanceJournal()
	issuer, err := NewPKIIssuer(ca, caSigner, "issuer-fixture-1", receiptPriv,
		map[string]ed25519.PublicKey{"approval-fixture-1": approvalPub}, hostPublic, journal, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return &lanesL204Harness{
		now: now, ca: ca, caSigner: caSigner, approvalPub: approvalPub, approvalPriv: approvalPriv,
		receiptPub: receiptPub, receiptPriv: receiptPriv, hostPublic: hostPublic, hostPrivate: hostPrivate,
		issuer: issuer, journal: journal,
	}
}

func pkixName(commonName string) pkix.Name { return pkix.Name{CommonName: commonName} }

func lanesL204ManagementContext(generation uint64) PKIIssuanceContext {
	return PKIIssuanceContext{Mode: "management", ManagementManifestID: "fixture-management-manifest-" + string(rune('0'+generation)), ManagementGeneration: generation}
}

func lanesL204BootstrapContext() PKIIssuanceContext {
	return PKIIssuanceContext{
		Mode: "bootstrap", BootstrapContractDigest: strings.Repeat("a", 64), BootstrapGeneration: 1,
		Installer: PKIInstallerArtifactTuple{
			SourceCommit: strings.Repeat("b", 40), ToolchainDigest: strings.Repeat("c", 64),
			InputDigest: strings.Repeat("d", 64), BinaryDigest: strings.Repeat("e", 64),
		}, BootstrapAttestationKeyID: "bootstrap-host-attestation-fixture",
	}
}

func (h *lanesL204Harness) issue(t *testing.T, hostID string, context PKIIssuanceContext) lanesL204Issued {
	t.Helper()
	plan, err := BuildPKIHostPlan(hostID)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewLocalPKIKeyStore(root)
	if err != nil {
		t.Fatal(err)
	}
	key, csrDER, err := store.CreateCSR(plan)
	if err != nil {
		t.Fatalf("create local fixture key and CSR for %s: %v", hostID, err)
	}
	h.mandateSeq++
	mandateOffset := time.Duration(h.mandateSeq) * time.Second
	mandate := PKIIssuanceMandate{
		HostID: hostID, Role: plan.Role, Context: context, URISAN: plan.URISAN, IP: plan.IP,
		CAProfile: plan.CAProfile, NotBefore: h.now.Add(-time.Minute).Add(mandateOffset), ExpiresAt: h.now.Add(time.Hour).Add(mandateOffset),
		CertificateNotAfter: h.now.Add(24 * time.Hour).Add(mandateOffset),
	}
	mandate, err = SignPKIIssuanceMandate(mandate, "approval-fixture-1", h.approvalPriv)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.journal.RegisterMandate(mandate); err != nil {
		t.Fatalf("register approved fixture mandate: %v", err)
	}
	request, err := NewPKICertificateRequest(plan, mandate, csrDER, key, h.hostPrivate[hostID])
	if err != nil {
		t.Fatalf("create host-signed public CSR request for %s: %v", hostID, err)
	}
	pkg, receipt, err := h.issuer.Issue(request)
	if err != nil {
		t.Fatalf("issue fixture certificate for %s: %v", hostID, err)
	}
	return lanesL204Issued{plan: plan, store: store, key: key, csr: csrDER, mandate: mandate, request: request, pkg: pkg, receipt: receipt}
}

func (h *lanesL204Harness) newMandate(t *testing.T, hostID string, context PKIIssuanceContext) PKIIssuanceMandate {
	t.Helper()
	mandate := h.signMandate(t, hostID, context)
	if err := h.journal.RegisterMandate(mandate); err != nil {
		t.Fatalf("register approved fixture mandate: %v", err)
	}
	return mandate
}

func (h *lanesL204Harness) signMandate(t *testing.T, hostID string, context PKIIssuanceContext) PKIIssuanceMandate {
	t.Helper()
	plan, err := BuildPKIHostPlan(hostID)
	if err != nil {
		t.Fatal(err)
	}
	h.mandateSeq++
	mandateOffset := time.Duration(h.mandateSeq) * time.Second
	mandate, err := SignPKIIssuanceMandate(PKIIssuanceMandate{
		HostID: hostID, Role: plan.Role, Context: context, URISAN: plan.URISAN, IP: plan.IP,
		CAProfile: plan.CAProfile, NotBefore: h.now.Add(-time.Minute).Add(mandateOffset), ExpiresAt: h.now.Add(time.Hour).Add(mandateOffset),
		CertificateNotAfter: h.now.Add(24 * time.Hour).Add(mandateOffset),
	}, "approval-fixture-1", h.approvalPriv)
	if err != nil {
		t.Fatal(err)
	}
	return mandate
}

func TestLanesL204LocalKeyCSRAndCertificatePackages(t *testing.T) {
	h := newLanesL204Harness(t)
	for _, host := range []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"} {
		issued := h.issue(t, host, lanesL204ManagementContext(1))
		keyInfo, err := os.Stat(issued.key.path)
		if err != nil || keyInfo.Mode().Perm() != 0o600 {
			t.Fatalf("%s local leaf key mode=%v err=%v; want 0600", host, keyInfo, err)
		}
		csr, err := x509.ParseCertificateRequest(issued.request.CSRDER)
		if err != nil || csr.CheckSignature() != nil || validatePKICSR(csr, issued.plan) != nil {
			t.Fatalf("%s CSR does not carry its exact fixed host identity: %v", host, err)
		}
		if issued.request.PublicKeyFingerprint != fingerprintPublicKey(csr.PublicKey) ||
			issued.pkg.HostID != host || issued.pkg.Role != issued.plan.Role || issued.pkg.RequestID != issued.request.ID ||
			len(issued.pkg.TrustRootsDER) != 1 || len(issued.pkg.CertificateDER) == 0 {
			t.Fatalf("%s request/package public identity mismatch", host)
		}
		var packageJSON []byte
		packageJSON, err = json.Marshal(struct {
			Request PKICertificateRequest `json:"request"`
			Package PKICertificatePackage `json:"package"`
		}{issued.request, issued.pkg})
		if err != nil || strings.Contains(string(packageJSON), "PRIVATE KEY") {
			t.Fatalf("%s request/package contains private-key material", host)
		}
		privatePEM, readErr := os.ReadFile(issued.key.path)
		privateBlock, _ := pem.Decode(privatePEM)
		if readErr != nil || privateBlock == nil || privateBlock.Type != "PRIVATE KEY" {
			t.Fatalf("%s installer did not retain a parseable local private key: %v", host, readErr)
		}
		if err := VerifyCertificatePackage(issued.pkg, issued.request, issued.plan, []*x509.Certificate{h.ca}, h.now); err != nil {
			t.Fatalf("%s valid certificate/trust package rejected: %v", host, err)
		}
		if err := VerifyPKIIssuerReceipt(issued.receipt, h.receiptPub, issued.pkg); err != nil {
			t.Fatalf("%s valid issuer receipt rejected: %v", host, err)
		}
	}
	if _, err := BuildPKIHostPlan("unknown-host"); err == nil {
		t.Fatal("unknown host received a PKI identity plan")
	}
}

func TestLanesL204FixedInstallerUsesOpaqueMandateAndFixedOutbox(t *testing.T) {
	h := newLanesL204Harness(t)
	mandate := h.newMandate(t, "linux-host", lanesL204ManagementContext(1))
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewLocalPKIKeyStore(root)
	if err != nil {
		t.Fatal(err)
	}
	outbox := &lanesL204Outbox{}
	installer, err := NewFixedPKIInstaller("linux-host", store,
		lanesL204MandateSource{mandates: map[string]PKIIssuanceMandate{mandate.ID: mandate}},
		lanesL204HostAttestor{key: h.hostPrivate["linux-host"]}, outbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := installer.RequestCertificate("../linux-host.key"); err == nil {
		t.Fatal("installer accepted a path-like value instead of an opaque mandate ID")
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("invalid opaque ID caused key creation: entries=%v err=%v", entries, err)
	}
	if err := installer.RequestCertificate(mandate.ID); err != nil {
		t.Fatalf("fixed opaque mandate request failed: %v", err)
	}
	if len(outbox.requests) != 1 || outbox.requests[0].HostID != "linux-host" ||
		outbox.requests[0].URISAN != "urn:remote-session-runner:release-transport:host:linux-host" ||
		outbox.requests[0].IP != "129.151.232.40" {
		t.Fatalf("fixed installer emitted an unexpected public request: %+v", outbox.requests)
	}
	if _, _, err := h.issuer.Issue(outbox.requests[0]); err != nil {
		t.Fatalf("issuer rejected fixed installer request: %v", err)
	}
	if err := installer.RequestCertificate(mandate.ID); err == nil || len(outbox.requests) != 1 {
		t.Fatal("installer replayed an already-used mandate or emitted a second request")
	}

	badHost := h.newMandate(t, "sandbox-host", lanesL204ManagementContext(1))
	badRoot := t.TempDir()
	if err := os.Chmod(badRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	badStore, err := NewLocalPKIKeyStore(badRoot)
	if err != nil {
		t.Fatal(err)
	}
	badOutbox := &lanesL204Outbox{}
	badInstaller, err := NewFixedPKIInstaller("linux-host", badStore,
		lanesL204MandateSource{mandates: map[string]PKIIssuanceMandate{badHost.ID: badHost}},
		lanesL204HostAttestor{key: h.hostPrivate["linux-host"]}, badOutbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := badInstaller.RequestCertificate(badHost.ID); err == nil || len(badOutbox.requests) != 0 {
		t.Fatal("fixed installer accepted a mandate for another host")
	}
	if entries, err := os.ReadDir(badRoot); err != nil || len(entries) != 0 {
		t.Fatalf("wrong-host mandate caused local key creation: entries=%v err=%v", entries, err)
	}
}

func TestLanesL204RejectsWrongHostSANCSRAndMandate(t *testing.T) {
	h := newLanesL204Harness(t)
	valid := h.issue(t, "linux-host", lanesL204ManagementContext(1))

	wrongHost := valid.request
	wrongHost.HostID = "sandbox-host"
	if _, _, err := h.issuer.Issue(wrongHost); err == nil {
		t.Fatal("request for another host was accepted")
	}
	wrongRole := valid.request
	wrongRole.Role = PKIReleaseAuthorityClient
	if _, _, err := h.issuer.Issue(wrongRole); err == nil {
		t.Fatal("request with the Mac client role on a Linux host was accepted")
	}

	wrongSANMandate := h.newMandate(t, "linux-host", lanesL204ManagementContext(2))
	wrongSANMandate.URISAN = "urn:remote-session-runner:release-transport:host:sandbox-host"
	wrongSANMandate, err := SignPKIIssuanceMandate(wrongSANMandate, "approval-fixture-1", h.approvalPriv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPKICertificateRequest(valid.plan, wrongSANMandate, valid.csr, valid.key, h.hostPrivate["linux-host"]); err == nil {
		t.Fatal("fixed installer accepted a mandate requesting another host SAN")
	}
	wrongCSRDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: valid.plan.CommonName}, URIs: []*url.URL{mustParseURI("urn:remote-session-runner:release-transport:host:sandbox-host")},
		IPAddresses: []net.IP{net.ParseIP(valid.plan.IP)},
	}, valid.key.signer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPKICertificateRequest(valid.plan, h.newMandate(t, "linux-host", lanesL204ManagementContext(2)), wrongCSRDER, valid.key, h.hostPrivate["linux-host"]); err == nil {
		t.Fatal("host-signed CSR with a wrong URI SAN was accepted")
	}

	wrongCSR := append([]byte(nil), valid.csr...)
	wrongCSR[len(wrongCSR)-1] ^= 0x01
	if _, err := NewPKICertificateRequest(valid.plan, h.newMandate(t, "linux-host", lanesL204ManagementContext(2)), wrongCSR, valid.key, h.hostPrivate["linux-host"]); err == nil {
		t.Fatal("tampered DER CSR was accepted")
	}

	wrongSignature := valid.request
	wrongSignature.Signature = append([]byte(nil), wrongSignature.Signature...)
	wrongSignature.Signature[0] ^= 0xff
	if _, _, err := h.issuer.Issue(wrongSignature); err == nil {
		t.Fatal("request with invalid host attestation signature was accepted")
	}

	future := h.newMandate(t, "linux-host", lanesL204ManagementContext(2))
	future.NotBefore = h.now.Add(time.Hour)
	future, err = SignPKIIssuanceMandate(future, "approval-fixture-1", h.approvalPriv)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.journal.RegisterMandate(future); err != nil {
		t.Fatalf("register future-dated fixture mandate: %v", err)
	}
	futureRequest := valid.request
	futureRequest.IssuanceMandateID = future.ID
	futureRequest.Context = future.Context
	futureRequest, err = lanesL204ResignRequest(futureRequest, h.hostPrivate["linux-host"])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.issuer.Issue(futureRequest); err == nil {
		t.Fatal("request under a future-dated unrelated mandate was accepted")
	}

	if _, _, err := h.issuer.Issue(valid.request); err == nil {
		t.Fatal("replayed one-use issuance mandate was accepted")
	}
}

func TestLanesL204BootstrapHandoffIsOneUseAndDisabledAfterHealth(t *testing.T) {
	h := newLanesL204Harness(t)
	first := h.issue(t, "linux-host", lanesL204BootstrapContext())
	handoff := lanesL204Handoff(first)
	gate := NewPKIBootstrapGate()
	if err := gate.AcceptBootstrapHandoff(handoff, h.receiptPub, h.hostPublic["linux-host"], h.now); err != nil {
		t.Fatalf("valid public-only root-console package handoff rejected: %v", err)
	}
	if err := gate.AcceptBootstrapHandoff(handoff, h.receiptPub, h.hostPublic["linux-host"], h.now); err == nil {
		t.Fatal("bootstrap handoff was accepted twice")
	}
	secondRoot := t.TempDir()
	if err := os.Chmod(secondRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	secondStore, err := NewLocalPKIKeyStore(secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, secondCSR, err := secondStore.CreateCSR(first.plan)
	if err != nil {
		t.Fatal(err)
	}
	secondMandate := h.signMandate(t, "linux-host", lanesL204BootstrapContext())
	if err := h.journal.RegisterMandate(secondMandate); err == nil {
		t.Fatal("issuer registered a second bootstrap mandate for a host with an issued certificate")
	}
	secondRequest, err := NewPKICertificateRequest(first.plan, secondMandate, secondCSR, secondKey, h.hostPrivate["linux-host"])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.issuer.Issue(secondRequest); err == nil {
		t.Fatal("issuer allowed a second bootstrap certificate for the same host")
	}
	if err := gate.MarkHealthy("sandbox-host"); err != nil {
		t.Fatal(err)
	}
	second := h.issue(t, "sandbox-host", lanesL204BootstrapContext())
	if err := gate.AcceptBootstrapHandoff(lanesL204Handoff(second), h.receiptPub, h.hostPublic["sandbox-host"], h.now); err == nil {
		t.Fatal("root-console bootstrap handoff remained enabled after first host health gate")
	}
	h3 := newLanesL204Harness(t)
	if err := h3.journal.RecordFirstHealthGate("factory-sandbox"); err != nil {
		t.Fatal(err)
	}
	mandate := h3.signMandate(t, "factory-sandbox", lanesL204BootstrapContext())
	if err := h3.journal.RegisterMandate(mandate); err == nil {
		t.Fatal("issuer registered a bootstrap mandate after the first host health gate")
	}
	bad := lanesL204Handoff(first)
	bad.Installer.BinaryDigest = strings.Repeat("f", 64)
	if err := NewPKIBootstrapGate().AcceptBootstrapHandoff(bad, h.receiptPub, h.hostPublic["linux-host"], h.now); err == nil {
		t.Fatal("handoff with altered bootstrap installer tuple was accepted")
	}
}

func TestLanesL204RejectsPackageAuthorizationAndRetiredPeers(t *testing.T) {
	h := newLanesL204Harness(t)
	issued := h.issue(t, "linux-host", lanesL204ManagementContext(1))
	client := h.issue(t, "mac-control-plane", lanesL204ManagementContext(1))
	route := releaseRouteForTest(t, "linux-host")
	active, err := ActivePeerFromPackage(route.PublicURL, client.plan, client.pkg)
	if err != nil {
		t.Fatal(err)
	}
	peerSet := PKIActivePeerSet{Endpoint: route.PublicURL, Active: active}
	peerDigest, err := PKIActivePeerSetDigest(peerSet)
	if err != nil {
		t.Fatal(err)
	}
	authorization := PKIInstallAuthorization{
		HostID: issued.plan.HostID, Role: issued.plan.Role, ManagementManifestID: "fixture-management-manifest-1",
		ManagementGeneration: 1, RequestID: issued.request.ID, IssuerReceiptID: issued.receipt.ID,
		PackageDigest: mustPackageDigest(issued.pkg), ActivePeerSetDigest: peerDigest,
	}
	if err := ValidateAuthorizedPKIPackage(issued.plan, issued.request, issued.pkg, issued.receipt, h.receiptPub,
		authorization, peerSet, issued.key, []*x509.Certificate{h.ca}, h.now); err != nil {
		t.Fatalf("exact management package authorization rejected: %v", err)
	}

	wrongPackage := issued.pkg
	wrongPackage.CertificateDER = append([]byte(nil), wrongPackage.CertificateDER...)
	wrongPackage.CertificateDER[len(wrongPackage.CertificateDER)-1] ^= 1
	if err := ValidateAuthorizedPKIPackage(issued.plan, issued.request, wrongPackage, issued.receipt, h.receiptPub,
		authorization, peerSet, issued.key, []*x509.Certificate{h.ca}, h.now); err == nil {
		t.Fatal("altered certificate package passed the authorization and receipt binding")
	}
	wrongAuthorization := authorization
	wrongAuthorization.PackageDigest = strings.Repeat("0", 64)
	if err := ValidateAuthorizedPKIPackage(issued.plan, issued.request, issued.pkg, issued.receipt, h.receiptPub,
		wrongAuthorization, peerSet, issued.key, []*x509.Certificate{h.ca}, h.now); err == nil {
		t.Fatal("authorization with a mismatched package digest was accepted")
	}
	wrongPeerSet := peerSet
	wrongPeerSet.Active.CertificateSHA256 = strings.Repeat("0", 64)
	if err := ValidateAuthorizedPKIPackage(issued.plan, issued.request, issued.pkg, issued.receipt, h.receiptPub,
		authorization, wrongPeerSet, issued.key, []*x509.Certificate{h.ca}, h.now); err == nil {
		t.Fatal("authorization with an altered active-peer-set digest was accepted")
	}
	old := h.issue(t, "linux-host", lanesL204ManagementContext(2))
	if err := validateActivePeerCertificate(mustParseLeaf(t, old.pkg), peerSet, h.now); err == nil {
		t.Fatal("still-chain-valid but retired leaf absent from active peer set was accepted")
	}
	peerSet.Overlap = []PKIActivePeer{{HostID: "linux-host", Role: PKIReleaseTransportServer, Endpoint: route.PublicURL,
		URISAN: issued.plan.URISAN, IP: issued.plan.IP, CertificateSHA256: old.pkg.CertificateSHA256,
		SPKISHA256: old.pkg.SPKISHA256, OverlapNotAfter: h.now.Add(time.Hour)}}
	if err := validateActivePeerCertificate(mustParseLeaf(t, old.pkg), peerSet, h.now); err != nil {
		t.Fatalf("bounded valid overlap leaf rejected: %v", err)
	}
	if err := validateActivePeerCertificate(mustParseLeaf(t, old.pkg), peerSet, h.now.Add(2*time.Hour)); err == nil {
		t.Fatal("expired overlap leaf remained active")
	}
}

func TestLanesL204NativeTLSVerifiesLiteralPublicIPsAndActivePeers(t *testing.T) {
	selectedHost := os.Getenv("LANES_NATIVE_HOST_ID")
	if selectedHost == "" {
		if os.Getenv("LANES_NATIVE_VALIDATION") == "1" {
			t.Fatal("LANES_NATIVE_HOST_ID must name this native validation host")
		}
		return
	}
	if selectedHost == "mac-control-plane" && runtime.GOOS != "darwin" {
		t.Fatalf("Mac PKI native identity selected on %s", runtime.GOOS)
	}
	if selectedHost != "mac-control-plane" && (runtime.GOOS != "linux" || (selectedHost != "linux-host" && selectedHost != "sandbox-host" && selectedHost != "factory-sandbox")) {
		t.Fatalf("invalid native PKI host %q on %s", selectedHost, runtime.GOOS)
	}
	h := newLanesL204Harness(t)
	client := h.issue(t, "mac-control-plane", lanesL204ManagementContext(1))
	for _, host := range []string{"linux-host", "sandbox-host", "factory-sandbox"} {
		route := releaseRouteForTest(t, host)
		server := h.issue(t, host, lanesL204ManagementContext(1))
		serverPeer, err := ActivePeerFromPackage(route.PublicURL, server.plan, server.pkg)
		if err != nil {
			t.Fatal(err)
		}
		clientPeer, err := ActivePeerFromPackage(route.PublicURL, client.plan, client.pkg)
		if err != nil {
			t.Fatal(err)
		}
		clientConfig, err := BuildReleaseTransportPKITLSClientConfig(route, client.pkg, client.key, server.pkg.TrustRootsDER,
			PKIActivePeerSet{Endpoint: route.PublicURL, Active: serverPeer}, h.now)
		if err != nil {
			t.Fatalf("%s fixed-IP client TLS config: %v", host, err)
		}
		serverConfig, err := BuildReleaseTransportPKITLSServerConfig(route, server.pkg, server.key, client.pkg.TrustRootsDER,
			PKIActivePeerSet{Endpoint: route.PublicURL, Active: clientPeer}, h.now)
		if err != nil {
			t.Fatalf("%s management TLS listener config: %v", host, err)
		}
		if clientConfig.ServerName != route.PublicIP || clientConfig.InsecureSkipVerify || clientConfig.MinVersion != tls.VersionTLS13 {
			t.Fatalf("%s client TLS config did not use ordinary literal-IP verification", host)
		}
		if err := lanesL204LoopbackHandshake(serverConfig, clientConfig); err != nil {
			t.Fatalf("%s local TLS handshake for %s: %v", host, route.PublicURL, err)
		}

		retired := h.issue(t, host, lanesL204ManagementContext(2))
		retiredConfig, err := BuildReleaseTransportPKITLSServerConfig(route, retired.pkg, retired.key, client.pkg.TrustRootsDER,
			PKIActivePeerSet{Endpoint: route.PublicURL, Active: clientPeer}, h.now)
		if err != nil {
			t.Fatalf("%s retired chain-valid listener fixture: %v", host, err)
		}
		if err := lanesL204LoopbackHandshake(retiredConfig, clientConfig); err == nil {
			t.Fatalf("%s TLS accepted a retired leaf that still chains to the trusted CA", host)
		}
	}
}

func lanesL204Handoff(issued lanesL204Issued) PKIBootstrapHandoff {
	context := issued.request.Context
	return PKIBootstrapHandoff{
		HostID: issued.plan.HostID, BootstrapContractDigest: context.BootstrapContractDigest,
		BootstrapGeneration: context.BootstrapGeneration, Installer: context.Installer,
		BootstrapAttestationKeyID: context.BootstrapAttestationKeyID, Request: issued.request,
		Package: issued.pkg, Receipt: issued.receipt,
	}
}

func releaseRouteForTest(t *testing.T, hostID string) ReleaseTransportTLSPlan {
	t.Helper()
	for _, route := range BuildReleaseTransportTLSPlans() {
		if route.HostID == hostID {
			return route
		}
	}
	t.Fatalf("missing fixed release-transport route for %s", hostID)
	return ReleaseTransportTLSPlan{}
}

func mustParseLeaf(t *testing.T, pkg PKICertificatePackage) *x509.Certificate {
	t.Helper()
	leaf, err := x509.ParseCertificate(pkg.CertificateDER)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func lanesL204LoopbackHandshake(serverConfig, clientConfig *tls.Config) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		tlsConnection := tls.Server(connection, serverConfig.Clone())
		handshakeErr := tlsConnection.Handshake()
		_ = tlsConnection.Close()
		serverDone <- handshakeErr
	}()
	connection, err := tls.Dial("tcp", listener.Addr().String(), clientConfig.Clone())
	if err == nil {
		_ = connection.Close()
	}
	serverErr := <-serverDone
	if err != nil {
		return err
	}
	return serverErr
}

func lanesL204ResignRequest(request PKICertificateRequest, hostKey ed25519.PrivateKey) (PKICertificateRequest, error) {
	request.ID, request.Signature = "", nil
	data, err := json.Marshal(request.payload())
	if err != nil {
		return PKICertificateRequest{}, err
	}
	id := sha256.Sum256(data)
	request.ID = hex.EncodeToString(id[:])
	request.Signature = ed25519.Sign(hostKey, signatureMessage(pkiRequestSignatureDomain, data))
	return request, nil
}
