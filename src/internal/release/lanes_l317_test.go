package release

import (
	"errors"
	"testing"
	"time"

	"remote-session-runner/src/internal/laneinstall"
)

func TestLanesL317AuthorityCohortPersistsIssuanceAndRequiresFreshAbandonRetry(t *testing.T) {
	f := l312NewPKIFixture(t)
	first := l312BootstrapContract(t, f.now, 1, 1)
	f.bootstrap = &first
	if err := f.journal.BeginBootstrapCohort(first, f.now); err != nil {
		t.Fatalf("persist approved first cohort: %v", err)
	}

	observed, expires := f.now.Add(-2*time.Minute), f.now.Add(3*time.Minute)
	if err := f.journal.RecordBootstrapCohortL0("mac-control-plane", l312EvidenceID(701), observed, expires, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.journal.RecordBootstrapCohortInstallation("mac-control-plane", l312EvidenceID(702), f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.journal.RecordBootstrapCohortHealth("mac-control-plane", l312EvidenceID(703), f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.journal.DisableBootstrapCohortException("mac-control-plane", l312EvidenceID(704)); err != nil {
		t.Fatal(err)
	}
	if err := f.journal.RecordBootstrapCohortL0("linux-host", l312EvidenceID(705), observed, expires, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.journal.RecordFirstHealthGate("linux-host"); !errors.Is(err, ErrAuthorityMandate) {
		t.Fatalf("unscoped legacy health marker bypassed cohort evidence: %v", err)
	}
	mandate, request, _, _, _ := f.issue("linux-host", true)
	firstState, err := f.journal.BootstrapCohortSnapshot()
	if err != nil || firstState.Validate() != nil || len(firstState.Attempts) != 1 ||
		firstState.Attempts[0].Hosts[1].IssuanceMandateID != mandate.ID ||
		firstState.Attempts[0].Hosts[1].CertificateRequestID != request.ID {
		t.Fatalf("cohort issuance not durably bound to the active host: state=%+v err=%v", firstState, err)
	}

	f.restartAuthorityJournal()
	if issued, healthy, err := f.journal.BootstrapState("linux-host"); err != nil || !issued || healthy {
		t.Fatalf("current bootstrap issuance state did not survive restart: issued=%t healthy=%t err=%v", issued, healthy, err)
	}
	if _, err := f.journal.mutate(func(state *authorityRootState) error {
		state.BootstrapCohort.Attempts[0].Hosts[1].CertificateRequestID = ""
		return nil
	}); !errors.Is(err, ErrAuthorityStateRollback) {
		t.Fatalf("authority journal allowed removal of retained cohort issuance evidence: %v", err)
	}
	if _, _, err := f.issuer.Issue(request); err == nil {
		t.Fatal("bootstrap request replay succeeded after authority restart")
	}
	if err := f.journal.BeginBootstrapCohort(l312BootstrapContract(t, f.now, 2, 2), f.now); !errors.Is(err, laneinstall.ErrInvalidBootstrapCohort) {
		t.Fatalf("new cohort began before whole-cohort abandonment: %v", err)
	}

	if err := f.journal.BeginBootstrapCohortAbandonment(l312EvidenceID(706), l312EvidenceID(707)); err != nil {
		t.Fatal(err)
	}
	for index, host := range []string{"mac-control-plane", "linux-host", "sandbox-host", "factory-sandbox"} {
		if err := f.journal.ConfirmBootstrapCohortAbandonDisable(host, l312EvidenceID(710+index)); err != nil {
			t.Fatalf("record whole-cohort disable for %s: %v", host, err)
		}
	}
	f.restartAuthorityJournal()
	ledger, err := f.journal.BootstrapCohortSnapshot()
	if err != nil || len(ledger.Attempts) != 1 || ledger.Attempts[0].Status != "abandoned" ||
		ledger.Attempts[0].Hosts[1].CertificateRequestID != request.ID {
		t.Fatalf("abandoned cohort or retained issuance was lost on restart: %+v err=%v", ledger, err)
	}

	second := l312BootstrapContract(t, f.now, 2, 2)
	if err := f.journal.BeginBootstrapCohort(second, f.now); err != nil {
		t.Fatalf("fresh generation and key pins did not permit retry: %v", err)
	}
	if issued, healthy, err := f.journal.BootstrapState("linux-host"); err != nil || issued || healthy {
		t.Fatalf("abandoned attempt leaked into retry bootstrap state: issued=%t healthy=%t err=%v", issued, healthy, err)
	}
	if err := f.journal.RegisterMandate(mandate); !errors.Is(err, ErrAuthorityReplay) {
		t.Fatalf("retry reused a prior generation mandate: %v", err)
	}
	ledger, err = f.journal.BootstrapCohortSnapshot()
	if err != nil || len(ledger.Attempts) != 2 || ledger.Attempts[0].Status != "abandoned" ||
		ledger.Attempts[1].Contract.Generation != 2 || ledger.Attempts[1].Contract.PublicPins == ledger.Attempts[0].Contract.PublicPins {
		t.Fatalf("retry changed history or reused cohort authority pins: %+v err=%v", ledger, err)
	}
}
