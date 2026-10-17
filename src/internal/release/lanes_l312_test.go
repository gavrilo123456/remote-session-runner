package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/laneinstall"
)

type l312PKIFixture struct {
	t          *testing.T
	journal    *AuthorityJournal
	dir        string
	now        time.Time
	ca         *x509.Certificate
	caKey      ed25519.PrivateKey
	approvPub  ed25519.PublicKey
	approvKey  ed25519.PrivateKey
	receiptPub ed25519.PublicKey
	receiptKey ed25519.PrivateKey
	hostPub    map[string]ed25519.PublicKey
	hostKey    map[string]ed25519.PrivateKey
	issuer     *laneinstall.PKIIssuer
	bootstrap  *laneinstall.BootstrapCohortContract
}

func l312NewPKIFixture(t *testing.T) *l312PKIFixture {
	t.Helper()
	authority := l310NewFixture(t)
	caPub, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := authority.now
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(312), Subject: pkix.Name{CommonName: "LANES-L3-12 fixture CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(2, 0, 0), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	approvPub, approvKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	receiptPub, receiptKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostPub := make(map[string]ed25519.PublicKey)
	hostKey := make(map[string]ed25519.PrivateKey)
	for _, host := range []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"} {
		public, private, keyErr := ed25519.GenerateKey(rand.Reader)
		if keyErr != nil {
			t.Fatal(keyErr)
		}
		hostPub[host], hostKey[host] = public, private
	}
	fixture := &l312PKIFixture{t: t, journal: authority.journal, dir: authority.dir, now: now, ca: ca, caKey: caKey,
		approvPub: approvPub, approvKey: approvKey, receiptPub: receiptPub, receiptKey: receiptKey,
		hostPub: hostPub, hostKey: hostKey}
	fixture.rebuildIssuer()
	return fixture
}

func (f *l312PKIFixture) rebuildIssuer() {
	f.t.Helper()
	issuer, err := laneinstall.NewPKIIssuer(f.ca, f.caKey, "issuer-fixture-l312", f.receiptKey,
		map[string]ed25519.PublicKey{"approval-l312": f.approvPub}, f.hostPub, f.journal, func() time.Time { return f.now })
	if err != nil {
		f.t.Fatal(err)
	}
	f.issuer = issuer
}

func (f *l312PKIFixture) restartAuthorityJournal() {
	f.t.Helper()
	if err := f.journal.Close(); err != nil {
		f.t.Fatal(err)
	}
	journal, err := openAuthorityJournalAt(f.dir, uint32(os.Getuid()), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	f.journal = journal
	f.t.Cleanup(func() { _ = journal.Close() })
	f.rebuildIssuer()
}

func (f *l312PKIFixture) context(host string, bootstrap bool) laneinstall.PKIIssuanceContext {
	if !bootstrap {
		return laneinstall.PKIIssuanceContext{Mode: "management", ManagementManifestID: "l312-management-" + host, ManagementGeneration: 1}
	}
	if f.bootstrap != nil {
		for _, contractHost := range f.bootstrap.Hosts {
			if contractHost.HostID != host {
				continue
			}
			for _, artifact := range contractHost.Artifacts {
				if artifact.Component == "runner-release-transport-pki-installer" {
					digest, err := f.bootstrap.Digest()
					if err != nil {
						f.t.Fatal(err)
					}
					return laneinstall.PKIIssuanceContext{Mode: "bootstrap", BootstrapContractDigest: digest, BootstrapGeneration: f.bootstrap.Generation,
						Installer: laneinstall.PKIInstallerArtifactTuple{SourceCommit: artifact.SourceCommit,
							ToolchainDigest: strings.TrimPrefix(artifact.ToolchainDigest, "sha256:"),
							InputDigest:     strings.TrimPrefix(artifact.BuildInputDigest, "sha256:"), BinaryDigest: strings.TrimPrefix(artifact.ArtifactDigest, "sha256:")},
						BootstrapAttestationKeyID: contractHost.BootstrapAttestationKeyID}
				}
			}
		}
	}
	return laneinstall.PKIIssuanceContext{Mode: "bootstrap", BootstrapContractDigest: strings.Repeat("a", 64), BootstrapGeneration: 1,
		Installer: laneinstall.PKIInstallerArtifactTuple{SourceCommit: strings.Repeat("b", 40), ToolchainDigest: strings.Repeat("c", 64),
			InputDigest: strings.Repeat("d", 64), BinaryDigest: strings.Repeat("e", 64)}, BootstrapAttestationKeyID: "l312-bootstrap-host-key"}
}

func l312BootstrapContract(t *testing.T, now time.Time, generation uint64, pinGeneration int) laneinstall.BootstrapCohortContract {
	t.Helper()
	contract := laneinstall.BootstrapCohortContract{ApprovalRecordID: fmt.Sprintf("%064x", 8000+pinGeneration), Generation: generation,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), SourceCommit: strings.Repeat("b", 40),
		GiteaCommit: strings.Repeat("b", 40), GitHubCommit: strings.Repeat("b", 40),
		PublicPins: laneinstall.BootstrapPublicKeyPins{AuthorityKeyID: fmt.Sprintf("authority-l312-%d", pinGeneration), AuthorityFingerprint: fmt.Sprintf("%064x", 100+pinGeneration),
			IssuerKeyID: fmt.Sprintf("issuer-l312-%d", pinGeneration), IssuerFingerprint: fmt.Sprintf("%064x", 200+pinGeneration), CACertificateFingerprint: fmt.Sprintf("%064x", 300+pinGeneration)}}
	for i, hostID := range []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"} {
		management, err := laneinstall.ManagementPackage(hostID)
		if err != nil {
			t.Fatal(err)
		}
		pkiPlan, err := laneinstall.BuildPKIHostPlan(hostID)
		if err != nil {
			t.Fatal(err)
		}
		root := "/var/lib/remote-session-runner/management"
		goos, goarch, group := "linux", "amd64", "root"
		if hostID == "mac-control-plane" {
			root, goos, goarch, group = "/Library/Application Support/RemoteSessionRunner/management", "darwin", "arm64", "wheel"
		} else if hostID == "sandbox-host" {
			goarch = "arm64"
		}
		bootstrapKeyID := fmt.Sprintf("l312-bootstrap-%d-%s", pinGeneration, hostID)
		host := laneinstall.BootstrapHostContract{HostID: hostID, ManagementRoot: root, PKI: pkiPlan,
			BootstrapAttestationKeyID: bootstrapKeyID, BootstrapKeyFingerprint: fmt.Sprintf("%064x", 2000+pinGeneration*10+i), ManagementPackage: management}
		for _, component := range management.Components {
			toolchain, inputs, artifact := fmt.Sprintf("%064x", 400+pinGeneration), fmt.Sprintf("%064x", 500+pinGeneration), fmt.Sprintf("%064x", 600+pinGeneration)
			if component.Name != "runner-release-transport-pki-installer" {
				toolchain, inputs, artifact = fmt.Sprintf("%064x", 700+pinGeneration*10+i), fmt.Sprintf("%064x", 800+pinGeneration*10+i), fmt.Sprintf("%064x", 900+pinGeneration*10+i)
			}
			host.Artifacts = append(host.Artifacts, laneinstall.BootstrapArtifactPin{Component: component.Name, GOOS: goos, GOARCH: goarch,
				InstallPath: filepath.Join(root, "bin", component.Name), Owner: "root", Group: group, Mode: "0755", SourceCommit: contract.SourceCommit,
				ToolchainDigest: "sha256:" + toolchain, BuildInputDigest: "sha256:" + inputs, ArtifactDigest: "sha256:" + artifact})
		}
		contract.Hosts = append(contract.Hosts, host)
	}
	if err := contract.Validate(now); err != nil {
		t.Fatalf("invalid L3-12 bootstrap fixture contract: %v", err)
	}
	return contract
}

func (f *l312PKIFixture) makeMandate(host string, bootstrap bool) laneinstall.PKIIssuanceMandate {
	f.t.Helper()
	mandate := f.signMandate(host, bootstrap)
	if err := f.journal.RegisterMandate(mandate); err != nil {
		f.t.Fatalf("register approved %s mandate: %v", host, err)
	}
	return mandate
}

func (f *l312PKIFixture) signMandate(host string, bootstrap bool) laneinstall.PKIIssuanceMandate {
	f.t.Helper()
	plan, err := laneinstall.BuildPKIHostPlan(host)
	if err != nil {
		f.t.Fatal(err)
	}
	f.now = f.now.Add(time.Second)
	mandate, err := laneinstall.SignPKIIssuanceMandate(laneinstall.PKIIssuanceMandate{
		HostID: host, Role: plan.Role, Context: f.context(host, bootstrap), URISAN: plan.URISAN, IP: plan.IP,
		CAProfile: plan.CAProfile, NotBefore: f.now.Add(-time.Minute), ExpiresAt: f.now.Add(time.Hour),
		CertificateNotAfter: f.now.Add(24 * time.Hour),
	}, "approval-l312", f.approvKey)
	if err != nil {
		f.t.Fatal(err)
	}
	return mandate
}

func (f *l312PKIFixture) request(host string, mandate laneinstall.PKIIssuanceMandate) (laneinstall.PKICertificateRequest, string) {
	f.t.Helper()
	root := f.t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		f.t.Fatal(err)
	}
	store, err := laneinstall.NewLocalPKIKeyStore(root)
	if err != nil {
		f.t.Fatal(err)
	}
	plan, err := laneinstall.BuildPKIHostPlan(host)
	if err != nil {
		f.t.Fatal(err)
	}
	key, csr, err := store.CreateCSR(plan)
	if err != nil {
		f.t.Fatal(err)
	}
	request, err := laneinstall.NewPKICertificateRequest(plan, mandate, csr, key, f.hostKey[host])
	if err != nil {
		f.t.Fatal(err)
	}
	keyPath := filepath.Join(root, host, string(plan.Role)+".pk8")
	return request, keyPath
}

func (f *l312PKIFixture) issue(host string, bootstrap bool) (laneinstall.PKIIssuanceMandate, laneinstall.PKICertificateRequest, laneinstall.PKICertificatePackage, laneinstall.PKIIssuerReceipt, string) {
	f.t.Helper()
	mandate := f.makeMandate(host, bootstrap)
	request, keyPath := f.request(host, mandate)
	pkg, receipt, err := f.issuer.Issue(request)
	if err != nil {
		f.t.Fatalf("issue %s certificate: %v", host, err)
	}
	return mandate, request, pkg, receipt, keyPath
}

func TestLanesL312NormalIssuanceBindsEveryHostTupleAndRetainsLocalKeys(t *testing.T) {
	f := l312NewPKIFixture(t)
	if _, ok := any(f.issuer).(laneinstall.PKIIssuanceMandateRegistrar); ok {
		t.Fatal("PKI issuer received the capability to create its own approval mandates")
	}
	if _, ok := any(f.issuer).(laneinstall.PKIBootstrapHealthRecorder); ok {
		t.Fatal("PKI issuer received the capability to assert host bootstrap health")
	}
	for _, host := range []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"} {
		mandate, request, pkg, receipt, keyPath := f.issue(host, false)
		plan, err := laneinstall.BuildPKIHostPlan(host)
		if err != nil || request.HostID != host || request.Role != plan.Role || request.URISAN != plan.URISAN || request.IP != plan.IP ||
			request.Context != mandate.Context || request.CAProfile != plan.CAProfile || request.CertificateNotAfter != mandate.CertificateNotAfter {
			t.Fatalf("%s issuance tuple differs from fixed host plan: request=%+v plan=%+v err=%v", host, request, plan, err)
		}
		keyInfo, err := os.Stat(keyPath)
		if err != nil || keyInfo.Mode().Perm() != 0o600 {
			t.Fatalf("%s leaf key mode=%v err=%v, want local 0600 key", host, keyInfo, err)
		}
		if err := laneinstall.VerifyCertificatePackage(pkg, request, plan, []*x509.Certificate{f.ca}, f.now); err != nil {
			t.Fatalf("%s certificate package failed verification: %v", host, err)
		}
		if err := laneinstall.VerifyPKIIssuerReceipt(receipt, f.receiptPub, pkg); err != nil {
			t.Fatalf("%s issuer receipt failed verification: %v", host, err)
		}
		publicMaterial, _ := json.Marshal(struct {
			Request laneinstall.PKICertificateRequest
			Package laneinstall.PKICertificatePackage
			Receipt laneinstall.PKIIssuerReceipt
		}{request, pkg, receipt})
		if strings.Contains(string(publicMaterial), "PRIVATE KEY") {
			t.Fatalf("%s public issuance material contains private-key text", host)
		}
		if host == "mac-control-plane" && (plan.IP != "" || plan.Endpoint != "local-socket") {
			t.Fatalf("Mac client unexpectedly received a public listener tuple: %+v", plan)
		}
	}
}

func TestLanesL312ExactReceiptSurvivesRestartAndMandateCannotReplay(t *testing.T) {
	f := l312NewPKIFixture(t)
	mandate, request, pkg, receipt, _ := f.issue("linux-host", false)
	state, err := f.journal.snapshot()
	if err != nil || len(state.PKIIssuanceMandates) != 1 || len(state.PKIIssuances) != 1 {
		t.Fatalf("issuance and non-secret receipt were not durably committed: mandates=%d issues=%d err=%v", len(state.PKIIssuanceMandates), len(state.PKIIssuances), err)
	}
	stored := state.PKIIssuances[mandate.ID]
	if stored.Request.ID != request.ID || stored.Package.CertificateSHA256 != pkg.CertificateSHA256 || stored.Receipt.ID != receipt.ID {
		t.Fatal("durable issuance record differs from the exact request/package/receipt")
	}
	storedBytes, err := json.Marshal(stored)
	if err != nil || strings.Contains(string(storedBytes), "PRIVATE KEY") {
		t.Fatalf("durable issuance journal contains private-key material: err=%v", err)
	}
	f.restartAuthorityJournal()
	loaded, err := f.journal.LookupPKIIssuanceMandate(mandate.ID)
	if err != nil || !samePKIIssuanceMandate(loaded, mandate) {
		t.Fatalf("exact mandate was not retained across issuer restart: err=%v", err)
	}
	if _, _, err := f.issuer.Issue(request); err == nil {
		t.Fatal("issuer replayed a previously consumed mandate after restart")
	}
	state, err = f.journal.snapshot()
	if err != nil || len(state.PKIIssuances) != 1 || state.PKIIssuances[mandate.ID].Receipt.ID != receipt.ID {
		t.Fatalf("replay changed or removed retained receipt: state=%+v err=%v", state.PKIIssuances, err)
	}
}

func TestLanesL312RejectsUnregisteredMismatchedAndUnsafeIssuanceEvidence(t *testing.T) {
	pending := l312NewPKIFixture(t)
	pendingMandate := pending.makeMandate("linux-host", false)
	pendingRequest, _ := pending.request("linux-host", pendingMandate)
	unsafeRequest := pendingRequest
	unsafeRequest.IP = "127.0.0.1"
	if _, _, err := pending.issuer.Issue(unsafeRequest); err == nil {
		t.Fatal("issuer accepted a CSR request whose IP tuple differed from the fixed host plan")
	}
	if state, err := pending.journal.snapshot(); err != nil || len(state.PKIIssuances) != 0 {
		t.Fatalf("rejected request consumed the mandate or recorded a receipt: err=%v", err)
	}
	if _, _, err := pending.issuer.Issue(pendingRequest); err != nil {
		t.Fatalf("valid exact request could not follow a rejected uncommitted request: %v", err)
	}

	f := l312NewPKIFixture(t)
	mandate, request, pkg, receipt, _ := f.issue("sandbox-host", false)
	if _, _, err := f.issuer.Issue(request); err == nil {
		t.Fatal("consumed normal mandate was reused")
	}
	wrong := request
	wrong.HostID = "factory-sandbox"
	if _, _, err := f.issuer.Issue(wrong); err == nil {
		t.Fatal("request with a caller-changed host was accepted")
	}
	wrong = request
	wrong.URISAN = "urn:remote-session-runner:release-transport:host:factory-sandbox"
	if _, _, err := f.issuer.Issue(wrong); err == nil {
		t.Fatal("request with another host's URI SAN was accepted")
	}
	wrong = request
	wrong.CSRDER = append([]byte(nil), request.CSRDER...)
	wrong.CSRDER[len(wrong.CSRDER)-1] ^= 1
	if _, _, err := f.issuer.Issue(wrong); err == nil {
		t.Fatal("request with a tampered CSR was accepted")
	}
	badRecord := laneinstall.PKIIssuanceJournalRecord{Mandate: mandate, Request: request, Package: pkg, Receipt: receipt}
	badRecord.Package.HostID = "factory-sandbox"
	if err := f.journal.CommitIssuance(badRecord); err == nil {
		t.Fatal("journal accepted package/receipt evidence for a different host")
	}
	if _, err := f.journal.mutate(func(state *authorityRootState) error {
		delete(state.PKIIssuanceMandates, mandate.ID)
		return nil
	}); !errors.Is(err, ErrAuthorityStateRollback) {
		t.Fatalf("journal allowed a consumed mandate to be removed: %v", err)
	}
	if _, err := f.journal.mutate(func(state *authorityRootState) error {
		record := state.PKIIssuances[mandate.ID]
		record.Receipt.ID = strings.Repeat("0", 64)
		state.PKIIssuances[mandate.ID] = record
		return nil
	}); !errors.Is(err, ErrAuthorityStateRollback) {
		t.Fatalf("journal allowed an issued receipt to be changed: %v", err)
	}
	if _, err := f.journal.LookupPKIIssuanceMandate(strings.Repeat("0", 64)); err == nil {
		t.Fatal("issuer journal resolved an unregistered mandate ID")
	}
	state, err := f.journal.snapshot()
	if err != nil || len(state.PKIIssuances) != 1 || state.PKIIssuances[mandate.ID].Receipt.ID != receipt.ID {
		t.Fatalf("rejected evidence changed durable issuance state: err=%v", err)
	}
}

func TestLanesL312BootstrapIsExactOneUseAndPublicOnly(t *testing.T) {
	f := l312NewPKIFixture(t)
	contract := l312BootstrapContract(t, f.now, 1, 1)
	f.bootstrap = &contract
	if err := f.journal.BeginBootstrapCohort(contract, f.now); err != nil {
		t.Fatalf("start durable bootstrap cohort: %v", err)
	}
	for index, host := range []string{"mac-control-plane", "linux-host", "sandbox-host"} {
		observed, expires := f.now.Add(-2*time.Minute), f.now.Add(3*time.Minute)
		if err := f.journal.RecordBootstrapCohortL0(host, l312EvidenceID(400+index), observed, expires, f.now); err != nil {
			t.Fatalf("record fresh L0 for %s: %v", host, err)
		}
		if host != "mac-control-plane" {
			f.issue(host, true)
		}
		if err := f.journal.RecordBootstrapCohortInstallation(host, l312EvidenceID(410+index), f.now); err != nil {
			t.Fatalf("record installation for %s: %v", host, err)
		}
		if err := f.journal.RecordBootstrapCohortHealth(host, l312EvidenceID(420+index), f.now); err != nil {
			t.Fatalf("record first health for %s: %v", host, err)
		}
		if err := f.journal.DisableBootstrapCohortException(host, l312EvidenceID(430+index)); err != nil {
			t.Fatalf("disable one-time exception for %s: %v", host, err)
		}
	}
	observed, expires := f.now.Add(-2*time.Minute), f.now.Add(3*time.Minute)
	if err := f.journal.RecordBootstrapCohortL0("factory-sandbox", l312EvidenceID(440), observed, expires, f.now); err != nil {
		t.Fatalf("record fresh L0 for factory-sandbox: %v", err)
	}
	mandate, request, pkg, receipt, _ := f.issue("factory-sandbox", true)
	contractDigest, _ := contract.Digest()
	if request.Context.Mode != "bootstrap" || request.Context.BootstrapContractDigest != contractDigest ||
		request.Context.BootstrapGeneration != 1 || request.Context.Installer.SourceCommit != strings.Repeat("b", 40) ||
		request.Context.BootstrapAttestationKeyID != "l312-bootstrap-1-factory-sandbox" {
		t.Fatalf("bootstrap request did not preserve the exact approved tuple: %+v", request.Context)
	}
	material, _ := json.Marshal(struct {
		Mandate laneinstall.PKIIssuanceMandate
		Request laneinstall.PKICertificateRequest
		Package laneinstall.PKICertificatePackage
		Receipt laneinstall.PKIIssuerReceipt
	}{mandate, request, pkg, receipt})
	if strings.Contains(string(material), "PRIVATE KEY") {
		t.Fatal("bootstrap handoff material contains a private key")
	}
	f.restartAuthorityJournal()
	if _, _, err := f.issuer.Issue(request); err == nil {
		t.Fatal("bootstrap certificate request replayed after issuer restart")
	}
	if issued, healthy, err := f.journal.BootstrapState("factory-sandbox"); err != nil || !issued || healthy {
		t.Fatalf("bootstrap one-use state did not survive restart: issued=%t healthy=%t err=%v", issued, healthy, err)
	}
}

func l312EvidenceID(n int) string { return fmt.Sprintf("%064x", n) }

func TestLanesL312FirstHealthGatePermanentlyClosesBootstrapAcrossRestart(t *testing.T) {
	f := l312NewPKIFixture(t)
	if err := f.journal.RecordFirstHealthGate("sandbox-host"); err != nil {
		t.Fatal(err)
	}
	f.restartAuthorityJournal()
	if issued, healthy, err := f.journal.BootstrapState("sandbox-host"); err != nil || issued || !healthy {
		t.Fatalf("first health gate was not durable: issued=%t healthy=%t err=%v", issued, healthy, err)
	}
	mandate := f.signMandate("sandbox-host", true)
	if err := f.journal.RegisterMandate(mandate); err == nil {
		t.Fatal("bootstrap mandate registration remained available after the first health gate")
	}
	if err := f.journal.RecordFirstHealthGate("mac-control-plane"); err == nil {
		t.Fatal("Mac client was accepted as a network-listener bootstrap health host")
	}
}
