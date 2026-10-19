package laneinstall

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

func TestCatalogCohortRequiresFreshScopedFencesAndFixedFourHostOrder(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	receipt := validCatalogCohortFixture(now)
	if err := ValidateCatalogCohortReceipt(receipt); err != nil {
		t.Fatalf("valid prepared cohort rejected: %v", err)
	}
	proof := strings.Repeat("f", 64)
	receipt, err := AdvanceCatalogCohortReceipt(receipt, "hold", "", proof, now)
	if err != nil || receipt.Stage != CatalogCohortHeld {
		t.Fatalf("hold stage=%q err=%v", receipt.Stage, err)
	}
	if _, err := AdvanceCatalogCohortReceipt(receipt, "switch", "sandbox-host", proof, now); err == nil {
		t.Fatal("out-of-order worker switch was accepted")
	}
	for _, host := range catalogCohortSwitchOrder {
		receipt, err = AdvanceCatalogCohortReceipt(receipt, "preflight", host, proof, now)
		if err != nil {
			t.Fatalf("fresh preflight %s: %v", host, err)
		}
		receipt, err = AdvanceCatalogCohortReceipt(receipt, "switch", host, proof, now)
		if err != nil {
			t.Fatalf("switch %s: %v", host, err)
		}
	}
	if receipt.Stage != CatalogCohortSwitching {
		t.Fatalf("post-switch stage=%q", receipt.Stage)
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "verify-start", "", proof, now)
	if err != nil || receipt.Stage != CatalogCohortVerifying {
		t.Fatalf("verify start stage=%q err=%v", receipt.Stage, err)
	}
	for _, host := range catalogCohortSwitchOrder {
		receipt, err = AdvanceCatalogCohortReceipt(receipt, "verify", host, proof, now)
		if err != nil {
			t.Fatalf("verify %s: %v", host, err)
		}
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "complete", "", proof, now)
	if err != nil || receipt.Stage != CatalogCohortComplete {
		t.Fatalf("complete stage=%q err=%v", receipt.Stage, err)
	}
	if _, err := AdvanceCatalogCohortReceipt(receipt, "block", "", proof, now); err == nil {
		t.Fatal("completed cohort was allowed to return to a blocked state")
	}
	if _, err := AdvanceCatalogCohortReceipt(receipt, "open", "linux-host", proof, now); err == nil {
		t.Fatal("cohort admission opened with a host-scoped transition")
	}
	released, err := AdvanceCatalogCohortReceipt(receipt, "open", "", proof, now)
	if err != nil || released.Stage != CatalogCohortReleased {
		t.Fatalf("open stage=%q err=%v", released.Stage, err)
	}
	if _, err := AdvanceCatalogCohortReceipt(released, "block", "", proof, now); err == nil {
		t.Fatal("released cohort was allowed to re-enter the maintenance state")
	}
}

func TestCatalogCohortRejectsStaleOrIncompleteOperationalEvidence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	stale := validCatalogCohortFixture(now.Add(-11 * time.Minute))
	if _, err := AdvanceCatalogCohortReceipt(stale, "hold", "", strings.Repeat("a", 64), now); err == nil {
		t.Fatal("stale preflight and fence evidence was accepted")
	}
	unpaused := validCatalogCohortFixture(now)
	unpaused.FenceProofs[0].Paused = false
	if err := ValidateCatalogCohortReceipt(unpaused); err == nil {
		t.Fatal("publisher pause without confirmation was accepted")
	}
	acceptedWork := validCatalogCohortFixture(now)
	acceptedWork.FenceProofs[0].NoNewAccepted = false
	if err := ValidateCatalogCohortReceipt(acceptedWork); err == nil {
		t.Fatal("fence proof that allows new accepted work was accepted")
	}
	insufficient := validCatalogCohortFixture(now)
	insufficient.Hosts[1].AvailableBytes = insufficient.Hosts[1].RequiredBytes - 1
	if err := ValidateCatalogCohortReceipt(insufficient); err == nil {
		t.Fatal("preflight with inadequate backup headroom was accepted")
	}
	missingBackup := validCatalogCohortFixture(now)
	missingBackup.Hosts[2].BackupManifestSHA256 = ""
	if err := ValidateCatalogCohortReceipt(missingBackup); err == nil {
		t.Fatal("preflight without a verified backup digest was accepted")
	}
}

func TestCatalogPRDCohortRequiresExactLegacyIdentityAndReadCompatibility(t *testing.T) {
	receipt := validCatalogCohortFixture(time.Now().UTC())
	receipt.Lane = config.ReleaseLanePRD
	for index := range receipt.Hosts {
		receipt.Hosts[index].PreviousTarget = ""
		receipt.Hosts[index].PreviousBundleSHA256 = strings.Repeat(string("abcdef"[index%6]), 64)
		receipt.Hosts[index].PreviousReadSchemaMax = receipt.Hosts[index].SchemaVersion
	}
	if err := ValidateCatalogCohortReceipt(receipt); err != nil {
		t.Fatalf("PRD cohort with an exact old-bin identity and compatible schema was rejected: %v", err)
	}
	unsafe := receipt
	unsafe.Hosts = append([]CatalogCohortHostPreflight(nil), receipt.Hosts...)
	unsafe.Hosts[2].PreviousReadSchemaMax = 0
	if err := ValidateCatalogCohortReceipt(unsafe); err == nil {
		t.Fatal("PRD cohort accepted legacy binaries without tested read-schema compatibility")
	}
	unsafe = receipt
	unsafe.Hosts = append([]CatalogCohortHostPreflight(nil), receipt.Hosts...)
	unsafe.Hosts[1].PreviousBundleSHA256 = ""
	if err := ValidateCatalogCohortReceipt(unsafe); err == nil {
		t.Fatal("PRD cohort accepted an unbound legacy binary inventory")
	}
}

func TestCatalogCohortReceiptsAreHashLinkedAndCanResumeOnlyAtRecordedStage(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	receipt := validCatalogCohortFixture(now)
	proof := strings.Repeat("c", 64)
	receipt, err := AdvanceCatalogCohortReceipt(receipt, "hold", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "block", "", proof, now)
	if err != nil || receipt.Stage != CatalogCohortBlocked {
		t.Fatalf("blocked stage=%q err=%v", receipt.Stage, err)
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "resume", "", proof, now)
	if err != nil || receipt.Stage != CatalogCohortHeld {
		t.Fatalf("resumed stage=%q err=%v", receipt.Stage, err)
	}
	if err := ValidateCatalogCohortReceipt(receipt); err != nil {
		t.Fatalf("hash-linked receipt rejected: %v", err)
	}
	receipt.Transitions[0].EvidenceSHA256 = strings.Repeat("d", 64)
	if err := ValidateCatalogCohortReceipt(receipt); err == nil {
		t.Fatal("tampered transition evidence passed the receipt hash chain")
	}
}

func TestCatalogCohortInterruptionAfterEveryHostSwitchKeepsAdmissionsHeld(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	proof := strings.Repeat("d", 64)
	for switchedCount := 1; switchedCount <= len(catalogCohortSwitchOrder); switchedCount++ {
		t.Run(catalogCohortSwitchOrder[switchedCount-1], func(t *testing.T) {
			receipt := validCatalogCohortFixture(now)
			var err error
			receipt, err = AdvanceCatalogCohortReceipt(receipt, "hold", "", proof, now)
			if err != nil {
				t.Fatal(err)
			}
			for index := 0; index < switchedCount; index++ {
				host := catalogCohortSwitchOrder[index]
				receipt, err = AdvanceCatalogCohortReceipt(receipt, "preflight", host, proof, now)
				if err != nil {
					t.Fatalf("preflight %s: %v", host, err)
				}
				receipt, err = AdvanceCatalogCohortReceipt(receipt, "switch", host, proof, now)
				if err != nil {
					t.Fatalf("switch %s: %v", host, err)
				}
			}
			receipt, err = AdvanceCatalogCohortReceipt(receipt, "block", "", proof, now)
			if err != nil || receipt.Stage != CatalogCohortBlocked || !catalogCohortStageHoldsAdmissions(receipt.Stage) {
				t.Fatalf("interrupted receipt stage=%q err=%v; selected-lane admissions must remain held", receipt.Stage, err)
			}
			if _, err := AdvanceCatalogCohortReceipt(receipt, "open", "", proof, now); err == nil {
				t.Fatal("interrupted cohort opened admissions before full verification or rollback")
			}
			if err := ValidateCatalogCohortReceipt(receipt); err != nil {
				t.Fatalf("interrupted receipt cannot be safely read and reconciled: %v", err)
			}
		})
	}
	for _, stage := range []CatalogCohortStage{CatalogCohortHeld, CatalogCohortSwitching, CatalogCohortVerifying,
		CatalogCohortComplete, CatalogCohortBlocked, CatalogCohortRollingBack, CatalogCohortRolledBack} {
		if !catalogCohortStageHoldsAdmissions(stage) {
			t.Errorf("maintenance stage %q does not hold lane admissions", stage)
		}
	}
	for _, stage := range []CatalogCohortStage{CatalogCohortPrepared, CatalogCohortReleased} {
		if catalogCohortStageHoldsAdmissions(stage) {
			t.Errorf("stage %q unexpectedly holds lane admissions", stage)
		}
	}
}

func TestCatalogCohortSwitchPreflightMustMatchTheExactLocalState(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	receipt, target := validCatalogCohortForSelectorFixture(t, fixture, now)
	proof := strings.Repeat("e", 64)
	var err error
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "hold", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "preflight", "linux-host", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	host := receipt.Hosts[0]
	if err := validateCatalogCohortHostMaySwitch(receipt, fixture.layout, fixture.legacyTarget, target,
		store.CurrentSchemaVersion, host.BackupDirectory, host.BackupManifestSHA256, now.Add(time.Minute)); err != nil {
		t.Fatalf("matching current preflight was rejected: %v", err)
	}
	if err := validateCatalogCohortHostMaySwitch(receipt, fixture.layout, "releases/"+strings.Repeat("9", 64), target,
		store.CurrentSchemaVersion, host.BackupDirectory, host.BackupManifestSHA256, now.Add(time.Minute)); err == nil {
		t.Fatal("preflight for a different selector predecessor was accepted")
	}
	if err := validateCatalogCohortHostMaySwitch(receipt, fixture.layout, fixture.legacyTarget, target,
		store.CurrentSchemaVersion, host.BackupDirectory, strings.Repeat("9", 64), now.Add(time.Minute)); err == nil {
		t.Fatal("preflight with a different backup identity was accepted")
	}
	if err := validateCatalogCohortHostMaySwitch(receipt, fixture.layout, fixture.legacyTarget, target,
		store.CurrentSchemaVersion, host.BackupDirectory, host.BackupManifestSHA256, now.Add(3*time.Minute)); err == nil {
		t.Fatal("expired local switch preflight was accepted")
	}
	if _, err := AdvanceCatalogCohortReceipt(receipt, "switch", "sandbox-host", proof, now); err == nil {
		t.Fatal("cohort receipt accepted a host that skipped the prescribed order")
	}
}

func validCatalogCohortForSelectorFixture(t *testing.T, fixture catalogSelectorFixture, now time.Time) (CatalogCohortReceipt, CatalogReleaseIdentity) {
	t.Helper()
	receipt := validCatalogCohortFixture(now)
	receipt.ReleaseID = fixture.firstRelease
	status, err := InspectCatalogRelease(fixture.catalogRoot, fixture.layout.hostID, fixture.firstRelease)
	if err != nil {
		t.Fatal(err)
	}
	receipt.ReleaseSHA256 = status.Identity.ReleaseSHA256
	for i := range receipt.Hosts {
		host := &receipt.Hosts[i]
		host.Target.ReleaseID = receipt.ReleaseID
		host.Target.ReleaseSHA256 = receipt.ReleaseSHA256
		host.PreflightAt = now.UTC().Format(time.RFC3339Nano)
	}
	receipt.Hosts[0].Target = status.Identity
	receipt.Hosts[0].PreviousTarget = fixture.legacyTarget
	receipt.Hosts[0].PreviousBundleSHA256 = filepath.Base(fixture.legacyTarget)
	configHash, err := hashCatalogConfiguration(fixture.layout.configFile)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Hosts[0].ConfigurationSHA256 = configHash
	backup := fixtureCatalogBackup(t, fixture.layout, fixture.legacyTarget, store.CurrentSchemaVersion)
	receipt.Hosts[0].BackupDirectory = backup.Directory
	receipt.Hosts[0].BackupManifestSHA256 = backup.ManifestSHA256
	if err := ValidateCatalogCohortReceipt(receipt); err != nil {
		t.Fatalf("prepared fixture receipt invalid: %v", err)
	}
	return receipt, status.Identity
}

func validCatalogCohortFixture(now time.Time) CatalogCohortReceipt {
	stamp := now.UTC().Format(time.RFC3339Nano)
	releaseSHA := strings.Repeat("a", 64)
	receipt := CatalogCohortReceipt{
		FormatVersion: catalogCohortFormatVersion, OperationID: strings.Repeat("1", 32),
		Lane: config.ReleaseLaneDev, ReleaseID: "v2.0.0", ReleaseSHA256: releaseSHA,
		SourceRevision: strings.Repeat("2", 40), CreatedAt: stamp, UpdatedAt: stamp,
		Stage: CatalogCohortPrepared,
		Database: CatalogDatabaseCompatibility{
			ReadSchemaMin: 1, ReadSchemaMax: store.CurrentSchemaVersion,
			WriteSchemaMin: store.CurrentSchemaVersion, WriteSchemaMax: store.CurrentSchemaVersion,
			Migration: catalogMigrationPolicy, Rollback: catalogRollbackPolicy,
		},
	}
	for index, hostID := range catalogCohortSwitchOrder {
		previous := strings.Repeat("3", 64)
		previousTarget := "releases/" + previous
		if hostID == "mac-control-plane" {
			previousTarget = "legacy-bin:" + previous
		}
		receipt.Hosts = append(receipt.Hosts, CatalogCohortHostPreflight{
			HostID: hostID, PreviousTarget: previousTarget, PreviousBundleSHA256: previous,
			Target:              CatalogReleaseIdentity{ReleaseID: receipt.ReleaseID, ReleaseSHA256: releaseSHA, HostID: hostID, BundleSHA256: strings.Repeat(string("456789abcdef"[index]), 64)},
			ConfigurationSHA256: strings.Repeat("4", 64), SchemaVersion: store.CurrentSchemaVersion,
			WorkInventorySHA256: strings.Repeat("5", 64), WorkInventoryEmpty: true, PreflightAt: stamp,
			BackupDirectory: "/private/tmp/catalog-cohort/" + hostID, BackupManifestSHA256: strings.Repeat("6", 64),
			AvailableBytes: 2 << 30, RequiredBytes: 1 << 30,
		})
	}
	for index, scope := range CatalogCohortRequiredFences() {
		receipt.FenceProofs = append(receipt.FenceProofs, CatalogCohortFenceProof{
			Scope: scope, Control: "isolated-fixture-control", Owner: "test-owner", Paused: true, NoNewAccepted: true,
			EvidenceSHA256: strings.Repeat(string("abcdef"[index%6]), 64), VerifiedAt: stamp,
		})
	}
	return receipt
}
