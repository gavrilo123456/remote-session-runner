package laneinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var lanesL317Hosts = []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"}

func TestLanesL317BootstrapContractPinsExactMatrixPathsAndPublicHostMapping(t *testing.T) {
	now := time.Date(2026, 10, 17, 15, 0, 0, 0, time.UTC)
	contract := lanesL317Contract(1, now, 1)
	if err := contract.Validate(now); err != nil {
		t.Fatalf("valid exact bootstrap cohort contract rejected: %v", err)
	}
	if digest, err := contract.Digest(); err != nil || !isManagementRecordID(digest) {
		t.Fatalf("exact public contract digest=%q err=%v", digest, err)
	}

	cases := []struct {
		name   string
		mutate func(*BootstrapCohortContract)
	}{
		{"host order", func(c *BootstrapCohortContract) { c.Hosts[0], c.Hosts[1] = c.Hosts[1], c.Hosts[0] }},
		{"wrong public route", func(c *BootstrapCohortContract) { c.Hosts[1].PKI.IP = "203.0.113.99" }},
		{"noncanonical UTC timestamp", func(c *BootstrapCohortContract) { c.NotBefore = c.NotBefore.In(time.FixedZone("utc-lookalike", 0)) }},
		{"wrong management root", func(c *BootstrapCohortContract) { c.Hosts[1].ManagementRoot = "/tmp/runner-management" }},
		{"unapproved artifact path", func(c *BootstrapCohortContract) {
			c.Hosts[2].Artifacts[0].InstallPath = filepath.Join(c.Hosts[2].ManagementRoot, "..", "lane", "runner")
		}},
		{"missing artifact tuple", func(c *BootstrapCohortContract) {
			c.Hosts[3].Artifacts = c.Hosts[3].Artifacts[:len(c.Hosts[3].Artifacts)-1]
		}},
		{"duplicate bootstrap key pin", func(c *BootstrapCohortContract) {
			c.Hosts[3].BootstrapKeyFingerprint = c.Hosts[2].BootstrapKeyFingerprint
		}},
		{"divergent source revision", func(c *BootstrapCohortContract) { c.Hosts[1].Artifacts[0].SourceCommit = strings.Repeat("f", 40) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := lanesL317Contract(1, now, 1)
			tc.mutate(&bad)
			if err := bad.Validate(now); !errors.Is(err, ErrInvalidBootstrapCohort) {
				t.Fatalf("unsafe bootstrap contract accepted: %v", err)
			}
		})
	}
	mutable := lanesL317Contract(1, now, 1)
	ledger, err := (BootstrapCohortLedger{}).Start(mutable, now)
	if err != nil {
		t.Fatal(err)
	}
	mutable.Hosts[0].Artifacts[0].InstallPath = "/tmp/replaced-after-start"
	if err := ledger.Validate(); err != nil {
		t.Fatalf("caller mutation changed the copied immutable cohort contract: %v", err)
	}
}

func TestLanesL317BootstrapCohortRequiresSerialFreshL0HealthAndPerHostExceptionDisable(t *testing.T) {
	now := time.Date(2026, 10, 17, 15, 0, 0, 0, time.UTC)
	contract := lanesL317Contract(1, now, 1)
	ledger, err := (BootstrapCohortLedger{}).Start(contract, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.DisableHostException("mac-control-plane", lanesL317ID(300)); !errors.Is(err, ErrBootstrapCohortOrder) {
		t.Fatalf("bootstrap exception disabled before host health: %v", err)
	}
	for index, host := range lanesL317Hosts {
		observed := now.Add(time.Duration(index) * time.Second)
		if index+1 < len(lanesL317Hosts) {
			if _, err := ledger.RecordFreshL0(lanesL317Hosts[index+1], lanesL317ID(110+index), observed, observed.Add(time.Minute), observed); !errors.Is(err, ErrBootstrapCohortOrder) {
				t.Fatalf("cohort skipped fixed host order at %s: %v", host, err)
			}
		}
		ledger, _, _ = lanesL317AdvanceHost(t, ledger, contract, index, observed, 100+index*100)
	}
	if err := ledger.Validate(); err != nil {
		t.Fatalf("completed serial cohort state is invalid: %v", err)
	}
	if ledger.Attempts[0].Status != BootstrapCohortReady || ledger.Attempts[0].NextHost != len(lanesL317Hosts) {
		t.Fatalf("complete host set did not reach ready-to-commit: %+v", ledger.Attempts[0])
	}
	completed, err := ledger.Complete(lanesL317ID(280), lanesL317ID(281))
	if err != nil || completed.Attempts[0].Status != BootstrapCohortComplete {
		t.Fatalf("complete approved cohort: status=%q err=%v", completed.Attempts[0].Status, err)
	}
	if _, err := completed.Start(lanesL317Contract(2, now, 2), now); !errors.Is(err, ErrInvalidBootstrapCohort) {
		t.Fatalf("a completed bootstrap baseline was allowed to start another bootstrap cohort: %v", err)
	}
}

func TestLanesL317PartialBootstrapFailureRequiresAllHostDisableBeforeFreshRetry(t *testing.T) {
	now := time.Date(2026, 10, 17, 15, 0, 0, 0, time.UTC)
	first := lanesL317Contract(1, now, 1)
	ledger, err := (BootstrapCohortLedger{}).Start(first, now)
	if err != nil {
		t.Fatal(err)
	}
	ledger, _, _ = lanesL317AdvanceHost(t, ledger, first, 0, now, 300)
	ledger, priorIssued, priorMandate := lanesL317AdvanceHost(t, ledger, first, 1, now.Add(time.Second), 400)
	ledger, err = ledger.BeginAbandonment(lanesL317ID(309), lanesL317ID(310))
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range lanesL317Hosts[:len(lanesL317Hosts)-1] {
		ledger, err = ledger.ConfirmAbandonDisable(host, lanesL317ID(320+bootstrapHostIndex(host)))
		if err != nil {
			t.Fatalf("record whole-cohort disable on %s: %v", host, err)
		}
	}
	if ledger.Attempts[0].Status != BootstrapCohortAbandoning {
		t.Fatalf("cohort became retryable before every host was disabled: %s", ledger.Attempts[0].Status)
	}
	if _, err := ledger.Start(lanesL317Contract(2, now, 2), now); !errors.Is(err, ErrInvalidBootstrapCohort) {
		t.Fatalf("new cohort started while an earlier host exception remained enabled: %v", err)
	}
	ledger, err = ledger.ConfirmAbandonDisable("factory-sandbox", lanesL317ID(330))
	if err != nil || ledger.Attempts[0].Status != BootstrapCohortAbandoned {
		t.Fatalf("cohort abandonment did not wait for all four disable proofs: status=%q err=%v", ledger.Attempts[0].Status, err)
	}
	if ledger.Attempts[0].Hosts[0].HealthEvidenceDigest == "" || ledger.Attempts[0].Hosts[0].InstallEvidenceDigest == "" {
		t.Fatal("abandonment discarded the already-healthy host's installation and evidence")
	}

	if _, err := ledger.Start(lanesL317Contract(1, now, 2), now); !errors.Is(err, ErrInvalidBootstrapCohort) {
		t.Fatalf("aborted generation was reused with new pins: %v", err)
	}
	if _, err := ledger.Start(lanesL317Contract(2, now, 1), now); !errors.Is(err, ErrInvalidBootstrapCohort) {
		t.Fatalf("retry reused authority, CA, issuer, or host bootstrap keys: %v", err)
	}
	second := lanesL317Contract(2, now, 2)
	ledger, err = ledger.Start(second, now)
	if err != nil {
		t.Fatalf("fresh approved generation with replacement public pins was rejected: %v", err)
	}
	_, err = ledger.RecordFreshL0(lanesL317Hosts[1], lanesL317ID(400), now, now.Add(time.Minute), now)
	if !errors.Is(err, ErrBootstrapCohortOrder) {
		t.Fatalf("retry reused first cohort L0 evidence: %v", err)
	}
	ledger, _, _ = lanesL317AdvanceHost(t, ledger, second, 0, now, 500)
	ledger, err = ledger.RecordFreshL0(lanesL317Hosts[1], lanesL317ID(501), now, now.Add(5*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest := mustLanesL317ContractDigest(t, second)
	secondContext := PKIIssuanceContext{Mode: "bootstrap", BootstrapContractDigest: secondDigest, BootstrapGeneration: 2,
		Installer: bootstrapInstallerTuple(second.Hosts[1]), BootstrapAttestationKeyID: second.Hosts[1].BootstrapAttestationKeyID}
	if _, err := ledger.RecordMandate(priorMandate); !errors.Is(err, ErrInvalidBootstrapCohort) {
		t.Fatalf("retry reused a prior host mandate: %v", err)
	}
	secondMandate := lanesL317Mandate(t, lanesL317Hosts[1], secondContext, now, now.Add(time.Minute))
	ledger, err = ledger.RecordMandate(secondMandate)
	if err != nil {
		t.Fatal(err)
	}
	installerDigest, _ := bootstrapInstallerTupleDigest(second.Hosts[1])
	_, err = ledger.RecordBootstrapIssuance(BootstrapPKIIssuanceEvidence{HostID: lanesL317Hosts[1], ContractDigest: secondDigest, Context: secondContext,
		IssuanceMandateID: secondMandate.ID, CertificateRequestID: priorIssued.CertificateRequestID, IssuerReceiptID: priorIssued.IssuerReceiptID,
		CertificatePackageDigest: priorIssued.CertificatePackageDigest, InstallerTupleDigest: installerDigest}, now)
	if !errors.Is(err, ErrInvalidBootstrapCohort) {
		t.Fatalf("retry reused a prior mandate/request/receipt/package: %v", err)
	}
}

func lanesL317Contract(generation uint64, now time.Time, pinGeneration int) BootstrapCohortContract {
	contract := BootstrapCohortContract{ApprovalRecordID: lanesL317ID(1000 + pinGeneration), Generation: generation,
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), SourceCommit: strings.Repeat("a", 40),
		GiteaCommit: strings.Repeat("a", 40), GitHubCommit: strings.Repeat("a", 40),
		PublicPins: BootstrapPublicKeyPins{AuthorityKeyID: fmt.Sprintf("authority-%d", pinGeneration), AuthorityFingerprint: lanesL317ID(1 + pinGeneration),
			IssuerKeyID: fmt.Sprintf("issuer-%d", pinGeneration), IssuerFingerprint: lanesL317ID(2 + pinGeneration), CACertificateFingerprint: lanesL317ID(3 + pinGeneration)}}
	for i, hostID := range lanesL317Hosts {
		management, _ := ManagementPackage(hostID)
		pkiPlan, _ := BuildPKIHostPlan(hostID)
		root := "/var/lib/remote-session-runner/management"
		if hostID == "mac-control-plane" {
			root = "/Library/Application Support/RemoteSessionRunner/management"
		}
		goos, goarch, group := bootstrapPlatform(hostID)
		host := BootstrapHostContract{HostID: hostID, ManagementRoot: root, PKI: pkiPlan,
			BootstrapAttestationKeyID: fmt.Sprintf("bootstrap-%d-%s", pinGeneration, hostID),
			BootstrapKeyFingerprint:   lanesL317ID(10 + pinGeneration*10 + i), ManagementPackage: management}
		for j, component := range management.Components {
			host.Artifacts = append(host.Artifacts, BootstrapArtifactPin{Component: component.Name,
				GOOS: goos, GOARCH: goarch, InstallPath: filepath.Join(root, "bin", component.Name), Owner: "root", Group: group, Mode: "0755",
				SourceCommit: contract.SourceCommit, ToolchainDigest: "sha256:" + lanesL317ID(100+i*10+j),
				BuildInputDigest: "sha256:" + lanesL317ID(200+i*10+j), ArtifactDigest: "sha256:" + lanesL317ID(300+pinGeneration*100+i*10+j)})
		}
		contract.Hosts = append(contract.Hosts, host)
	}
	return contract
}

func lanesL317AdvanceHost(t *testing.T, ledger BootstrapCohortLedger, contract BootstrapCohortContract, index int, observed time.Time, idBase int) (BootstrapCohortLedger, BootstrapPKIIssuanceEvidence, PKIIssuanceMandate) {
	t.Helper()
	host := lanesL317Hosts[index]
	expires := observed.Add(5 * time.Minute)
	var err error
	ledger, err = ledger.RecordFreshL0(host, lanesL317ID(idBase), observed, expires, observed)
	if err != nil {
		t.Fatalf("record fresh L0 for %s: %v", host, err)
	}
	var evidence BootstrapPKIIssuanceEvidence
	var mandate PKIIssuanceMandate
	if host != "mac-control-plane" {
		hostContract := contract.Hosts[index]
		context := PKIIssuanceContext{Mode: "bootstrap", BootstrapContractDigest: mustLanesL317ContractDigest(t, contract),
			BootstrapGeneration: contract.Generation, Installer: bootstrapInstallerTuple(hostContract), BootstrapAttestationKeyID: hostContract.BootstrapAttestationKeyID}
		mandate = lanesL317Mandate(t, host, context, observed, observed.Add(time.Minute))
		ledger, err = ledger.RecordMandate(mandate)
		if err != nil {
			t.Fatalf("record one-use bootstrap mandate on %s: %v", host, err)
		}
		installerDigest, digestErr := bootstrapInstallerTupleDigest(hostContract)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		evidence = BootstrapPKIIssuanceEvidence{HostID: host, ContractDigest: context.BootstrapContractDigest, Context: context,
			IssuanceMandateID: mandate.ID, CertificateRequestID: lanesL317ID(idBase + 10), IssuerReceiptID: lanesL317ID(idBase + 20),
			CertificatePackageDigest: lanesL317ID(idBase + 30), InstallerTupleDigest: installerDigest}
		ledger, err = ledger.RecordBootstrapIssuance(evidence, observed)
		if err != nil {
			t.Fatalf("record bootstrap issuance on %s: %v", host, err)
		}
	}
	ledger, err = ledger.RecordInstalled(host, lanesL317ID(idBase+40), observed)
	if err != nil {
		t.Fatalf("record exact installation on %s: %v", host, err)
	}
	ledger, err = ledger.RecordHealthy(host, lanesL317ID(idBase+50), observed)
	if err != nil {
		t.Fatalf("record health on %s: %v", host, err)
	}
	ledger, err = ledger.DisableHostException(host, lanesL317ID(idBase+60))
	if err != nil {
		t.Fatalf("disable host exception on %s: %v", host, err)
	}
	return ledger, evidence, mandate
}

func lanesL317ID(number int) string { return fmt.Sprintf("%064x", number) }

func lanesL317Mandate(t *testing.T, host string, context PKIIssuanceContext, notBefore, expires time.Time) PKIIssuanceMandate {
	t.Helper()
	plan, err := BuildPKIHostPlan(host)
	if err != nil {
		t.Fatal(err)
	}
	mandate := PKIIssuanceMandate{ApprovalKeyID: "fixture-approval", HostID: host, Role: plan.Role, Context: context,
		URISAN: plan.URISAN, IP: plan.IP, CAProfile: plan.CAProfile, NotBefore: notBefore, ExpiresAt: expires,
		CertificateNotAfter: expires.Add(time.Hour), Signature: make([]byte, 64)}
	data, err := json.Marshal(mandate.payload())
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	mandate.ID = hex.EncodeToString(digest[:])
	return mandate
}

func mustLanesL317ContractDigest(t *testing.T, contract BootstrapCohortContract) string {
	t.Helper()
	digest, err := contract.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
