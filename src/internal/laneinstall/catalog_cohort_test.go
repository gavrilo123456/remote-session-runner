package laneinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
	unpaused.FenceProofs[catalogCohortFenceProofIndex(unpaused.FenceProofs, "mac-project-publishers")].Paused = false
	if err := ValidateCatalogCohortReceipt(unpaused); err == nil {
		t.Fatal("publisher pause without confirmation was accepted")
	}
	acceptedWork := validCatalogCohortFixture(now)
	acceptedWork.FenceProofs[catalogCohortFenceProofIndex(acceptedWork.FenceProofs, "mac-project-publishers")].NoNewAccepted = false
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
	for _, proof := range receipt.FenceProofs {
		if proof.Scope == "mac-project-publishers" {
			receipt.FenceProofs = []CatalogCohortFenceProof{proof}
			break
		}
	}
	for index := range receipt.Hosts {
		receipt.Hosts[index].PreviousTarget = ""
		receipt.Hosts[index].PreviousBundleSHA256 = strings.Repeat(string("abcdef"[index%6]), 64)
		receipt.Hosts[index].LegacyRollback = validLegacyRollbackEvidence(receipt.Hosts[index].PreviousBundleSHA256,
			receipt.Hosts[index].ConfigurationSHA256, receipt.Hosts[index].SchemaVersion, time.Now().UTC())
		receipt.Hosts[index].PreviousReadSchemaMax = receipt.Hosts[index].LegacyRollback.ReadSchemaMax
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
	unsafe = receipt
	unsafe.Hosts = append([]CatalogCohortHostPreflight(nil), receipt.Hosts...)
	unsafe.Hosts[0].LegacyRollback.ConfigurationSHA256 = strings.Repeat("a", 64)
	if err := ValidateCatalogCohortReceipt(unsafe); err == nil {
		t.Fatal("PRD cohort accepted compatibility evidence bound to another configuration")
	}
	unsafe = receipt
	unsafe.Hosts = append([]CatalogCohortHostPreflight(nil), receipt.Hosts...)
	unsafe.Hosts[1].LegacyRollback.SemanticChecks = []string{"current-database-read-without-migration"}
	if err := ValidateCatalogCohortReceipt(unsafe); err == nil {
		t.Fatal("PRD cohort accepted incomplete rollback semantic-test evidence")
	}
}

func TestCatalogPRDCohortPreparedFenceThenHoldRequiresEveryStoppedHostInOrder(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	manifest, reports := catalogCohortCreationFixture(t, now)
	for index := range reports {
		host := &reports[index].Preflight
		host.PreviousTarget = ""
		host.PreviousBundleSHA256 = strings.Repeat(string("abcdef"[index%6]), 64)
		host.LegacyRollback = validLegacyRollbackEvidence(host.PreviousBundleSHA256, host.ConfigurationSHA256, host.SchemaVersion, now)
		host.PreviousReadSchemaMax = host.LegacyRollback.ReadSchemaMax
		host.ServiceActiveAtPreflight = true
	}
	receipt, err := NewCatalogCohortReceipt(config.ReleaseLanePRD, manifest, reports,
		CatalogCohortPublisherPause{Owner: "owner", Control: "publisher maintenance pause", Paused: true, NoNewAccepted: true}, now)
	if err != nil {
		t.Fatalf("create prepared PRD receipt: %v", err)
	}
	if len(receipt.FenceProofs) != 1 || receipt.Stage != CatalogCohortPrepared {
		t.Fatalf("initial PRD receipt asserted unproven host fences: stage=%s fences=%+v", receipt.Stage, receipt.FenceProofs)
	}
	proof := strings.Repeat("e", 64)
	if _, err := AdvanceCatalogCohortReceipt(receipt, "hold", "", proof, now); err == nil {
		t.Fatal("PRD admission hold was allowed before every host had a stop proof")
	}
	for _, hostID := range catalogCohortSwitchOrder {
		index := cohortHostIndex(receipt, hostID)
		host := receipt.Hosts[index]
		fence := CatalogCohortHostFence{HostID: hostID, ServiceWasActive: true, ServiceStopped: true,
			WorkInventorySHA256: host.WorkInventorySHA256, ConfigurationSHA256: host.ConfigurationSHA256,
			SelectorTarget: host.PreviousTarget, PreviousBundleSHA256: host.PreviousBundleSHA256,
			DatabaseSchema: host.SchemaVersion, ObservedAt: now.Format(time.RFC3339Nano)}
		receipt, err = AdvanceCatalogCohortHostFence(receipt, fence, now)
		if err != nil {
			t.Fatalf("record host fence %s: %v", hostID, err)
		}
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "hold", "", proof, now)
	if err != nil || receipt.Stage != CatalogCohortHeld {
		t.Fatalf("record initial PRD hold after all host fences: stage=%s err=%v", receipt.Stage, err)
	}
}

func validLegacyRollbackEvidence(bundleSHA, configSHA string, schema int, at time.Time) *CatalogLegacyRollbackCompatibility {
	return &CatalogLegacyRollbackCompatibility{
		FormatVersion: 1, LegacyBundleSHA256: bundleSHA, ConfigurationSHA256: configSHA,
		DatabaseSchema: schema, ReadSchemaMin: 1, ReadSchemaMax: schema,
		TestSuite: catalogLegacyRollbackTestSuite, TestEvidenceSHA256: strings.Repeat("9", 64),
		SemanticChecks: append([]string(nil), catalogLegacyRollbackSemanticChecks...), TestedAt: at.UTC().Format(time.RFC3339Nano),
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
	for _, stage := range []CatalogCohortStage{CatalogCohortPrepared, CatalogCohortReleased, CatalogCohortSuperseded} {
		if catalogCohortStageHoldsAdmissions(stage) {
			t.Errorf("stage %q unexpectedly holds lane admissions", stage)
		}
	}
}

func TestCatalogCohortSupersedesAbandonedPreSwitchAttemptsAndThenAllowsSuccessor(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	proof := strings.Repeat("f", 64)
	successor := heldCatalogCohortFixture(t, now, strings.Repeat("b", 32), "v2.0.1")
	oldAttempts := []CatalogCohortReceipt{
		validCatalogCohortFixture(now),
		validCatalogCohortFixture(now),
		validCatalogCohortFixture(now),
		validCatalogCohortFixture(now),
	}
	oldAttempts[1].OperationID = strings.Repeat("c", 32)
	oldAttempts[2].OperationID = strings.Repeat("d", 32)
	oldAttempts[3].OperationID = strings.Repeat("e", 32)
	var err error
	oldAttempts[0], err = AdvanceCatalogCohortReceipt(oldAttempts[0], "hold", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	oldAttempts[0], err = AdvanceCatalogCohortReceipt(oldAttempts[0], "preflight", "linux-host", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	oldAttempts[1], err = AdvanceCatalogCohortReceipt(oldAttempts[1], "hold", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	oldAttempts[1], err = AdvanceCatalogCohortReceipt(oldAttempts[1], "block", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	// This abandoned attempt was blocked before it ever recorded a hold. The
	// source guard still counts blocked receipts conservatively, so it must use
	// the same verified supersession path instead of blocking later releases.
	oldAttempts[2], err = AdvanceCatalogCohortReceipt(oldAttempts[2], "block", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	// A prepared attempt can be retired only after each host verifies that its
	// selector and local operation journal still match the prepared evidence.
	if oldAttempts[3].Stage != CatalogCohortPrepared || len(oldAttempts[3].Transitions) != 0 {
		t.Fatalf("fixture must be an abandoned pre-hold attempt: stage=%q transitions=%d", oldAttempts[3].Stage, len(oldAttempts[3].Transitions))
	}
	plan, err := BuildSimplePlan("mac-control-plane", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	cohortRoot := filepath.Join(t.TempDir(), "catalog-cohorts")
	if err := os.Mkdir(cohortRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyCohortReceiptFixture(t, cohortRoot, oldAttempts[0])
	writeLegacyCohortReceiptFixture(t, cohortRoot, oldAttempts[1])
	writeLegacyCohortReceiptFixture(t, cohortRoot, oldAttempts[2])
	writeLegacyCohortReceiptFixture(t, cohortRoot, oldAttempts[3])
	writeCohortReceiptFixture(t, cohortRoot, successor)
	if _, _, err := catalogCohortAdmissionHeldAt(plan, cohortRoot); err == nil {
		t.Fatal("multiple abandoned receipts did not fail closed before supported reconciliation")
	}

	for attemptIndex, old := range oldAttempts {
		for hostIndex, hostID := range catalogCohortSwitchOrder {
			observation := fixtureCohortSupersessionEvidence(old, successor, hostIndex, "none")
			observation.HostID = hostID
			old, err = AdvanceCatalogCohortSupersedeHost(old, successor, observation, now)
			if err != nil {
				t.Fatalf("attempt %d host %s supersede observation: %v", attemptIndex, hostID, err)
			}
		}
		if old.Stage == CatalogCohortPrepared && catalogCohortStageHoldsAdmissions(old.Stage) {
			t.Fatalf("attempt %d prepared stage unexpectedly holds admissions", attemptIndex)
		}
		if old.Stage != CatalogCohortPrepared && !catalogCohortStageHoldsAdmissions(old.Stage) {
			t.Fatalf("attempt %d released its hold before explicit finalization", attemptIndex)
		}
		old, err = AdvanceCatalogCohortSupersede(old, successor.OperationID, now)
		if err != nil || old.Stage != CatalogCohortSuperseded || catalogCohortStageHoldsAdmissions(old.Stage) {
			t.Fatalf("attempt %d stage=%q err=%v; completed historical hold must be retired", attemptIndex, old.Stage, err)
		}
		if err := ValidateCatalogCohortReceipt(old); err != nil {
			t.Fatalf("attempt %d superseded receipt lost its valid history: %v", attemptIndex, err)
		}
		writeLegacyCohortReceiptFixture(t, cohortRoot, old)
	}
	if !catalogCohortStageHoldsAdmissions(successor.Stage) {
		t.Fatal("valid successor stopped holding admissions while still in progress")
	}
	held, operationID, err := catalogCohortAdmissionHeldAt(plan, cohortRoot)
	if err != nil || !held || operationID != successor.OperationID {
		t.Fatalf("reconciled history still blocks a unique valid successor: held=%t operation=%q err=%v", held, operationID, err)
	}
	for _, hostID := range catalogCohortSwitchOrder {
		successor, err = AdvanceCatalogCohortReceipt(successor, "preflight", hostID, proof, now)
		if err != nil {
			t.Fatalf("successor preflight %s: %v", hostID, err)
		}
		successor, err = AdvanceCatalogCohortReceipt(successor, "switch", hostID, proof, now)
		if err != nil {
			t.Fatalf("successor switch %s: %v", hostID, err)
		}
	}
	if successor.Stage != CatalogCohortSwitching || !catalogCohortStageHoldsAdmissions(successor.Stage) {
		t.Fatalf("successor stage=%q did not advance with its hold intact", successor.Stage)
	}
}

func TestReadCatalogCohortReceiptAcceptsOnlyValidatedLegacyCanonicalEncoding(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	receipt := validCatalogCohortFixture(now)
	receipt.OperationID = strings.Repeat("a", 32)
	var err error
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "hold", "", strings.Repeat("b", 64), now)
	if err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(t.TempDir(), "catalog-cohorts")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeLegacyCohortReceiptFixture(t, root, receipt)
	plan, err := BuildCatalogPlan("mac-control-plane", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := readCatalogCohortReceipt(filepath.Join(root, receipt.OperationID+".json"))
	if err != nil || !reflect.DeepEqual(loaded, receipt) {
		t.Fatalf("valid legacy receipt was not preserved: equal=%t err=%v", reflect.DeepEqual(loaded, receipt), err)
	}
	held, operationID, err := catalogCohortAdmissionHeldAt(plan, root)
	if err != nil || !held || operationID != receipt.OperationID {
		t.Fatalf("legacy receipt hold was not surfaced exactly: held=%t operation=%q err=%v", held, operationID, err)
	}

	// Keep the old encoding but corrupt a transition digest. Compatibility must
	// not weaken the existing chain-integrity validation.
	corrupt := receipt
	corrupt.Transitions = append([]CatalogCohortTransition(nil), receipt.Transitions...)
	corrupt.Transitions[0].EvidenceSHA256 = strings.Repeat("c", 64)
	writeLegacyCohortReceiptFixture(t, root, corrupt)
	if _, err := readCatalogCohortReceipt(filepath.Join(root, receipt.OperationID+".json")); err == nil {
		t.Fatal("legacy encoding with an invalid transition was accepted")
	}
}

func TestCatalogCohortReceiptConstructionBindsSupportedPreflightsAndOwnerPause(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	manifest, reports := catalogCohortCreationFixture(t, now)
	pause := CatalogCohortPublisherPause{
		Owner: "Tomasz Walczuk", Control: "Owner instruction keeps project publishers paused for the Dev cohort switch.",
		Paused: true, NoNewAccepted: true,
	}
	receipt, err := NewCatalogCohortReceipt(config.ReleaseLaneDev, manifest, reports, pause, now)
	if err != nil {
		t.Fatalf("construct supported prepared receipt: %v", err)
	}
	if err := ValidateCatalogCohortReceipt(receipt); err != nil {
		t.Fatalf("constructed receipt failed validation: %v", err)
	}
	if catalogCohortFenceProofIndex(receipt.FenceProofs, "project-publishers:dev") < 0 {
		t.Fatalf("Dev receipt did not bind its publisher fence to Dev only: %+v", receipt.FenceProofs)
	}
	manifestBytes, err := CatalogReleaseManifestBytes(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	if receipt.Stage != CatalogCohortPrepared || len(receipt.Transitions) != 0 || receipt.ReleaseSHA256 != hex.EncodeToString(manifestDigest[:]) ||
		receipt.SourceRevision != manifest.SourceRevision || len(receipt.Hosts) != len(catalogCohortSwitchOrder) {
		t.Fatalf("constructed receipt did not bind a prepared four-host release: %+v", receipt)
	}
	for _, hostID := range catalogCohortSwitchOrder {
		if !cohortContainsHost(receipt, hostID) {
			t.Fatalf("constructed receipt omitted %s", hostID)
		}
	}
	badReports := append([]CatalogCohortHostPreflightReport(nil), reports...)
	badReports[0].Preflight.Target.BundleSHA256 = strings.Repeat("f", 64)
	if _, err := NewCatalogCohortReceipt(config.ReleaseLaneDev, manifest, badReports, pause, now); err == nil {
		t.Fatal("receipt accepted a host preflight for different sealed bundle bytes")
	}
	badReports = append([]CatalogCohortHostPreflightReport(nil), reports...)
	badReports[0].Preflight.PreflightAt = now.Add(-11 * time.Minute).Format(time.RFC3339Nano)
	if _, err := NewCatalogCohortReceipt(config.ReleaseLaneDev, manifest, badReports, pause, now); err == nil {
		t.Fatal("receipt accepted an expired host backup/preflight")
	}
	badReports = append([]CatalogCohortHostPreflightReport(nil), reports...)
	badReports[1].WorkInventory.QueuedCommands = 1
	if _, err := NewCatalogCohortReceipt(config.ReleaseLaneDev, manifest, badReports, pause, now); err == nil {
		t.Fatal("receipt accepted a nonempty restart inventory")
	}
	if _, err := NewCatalogCohortReceipt(config.ReleaseLaneDev, manifest, reports,
		CatalogCohortPublisherPause{Owner: pause.Owner, Control: pause.Control, Paused: true}, now); err == nil {
		t.Fatal("receipt accepted an owner attestation without no-new-accepted confirmation")
	}
}

func TestCatalogCohortPublisherFenceIsLaneScopedAndLegacyGlobalReceiptsRemainValid(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	manifest, reports := catalogCohortCreationFixture(t, now)
	for _, lane := range []config.ReleaseLane{config.ReleaseLaneDev, config.ReleaseLaneQAS, config.ReleaseLanePRD} {
		receipt, err := NewCatalogCohortReceipt(lane, manifest, reports, CatalogCohortPublisherPause{
			Owner: "owner", Control: "selected lane publisher fence", Paused: true, NoNewAccepted: true,
		}, now)
		if err != nil {
			t.Fatalf("create %s receipt: %v", lane, err)
		}
		if catalogCohortFenceProofIndex(receipt.FenceProofs, "project-publishers:"+string(lane)) < 0 {
			t.Fatalf("%s receipt has the wrong publisher fence scope: %+v", lane, receipt.FenceProofs)
		}
		if err := ValidateCatalogCohortReceipt(receipt); err != nil {
			t.Fatalf("validate %s lane-scoped receipt: %v", lane, err)
		}
	}
	legacy := validCatalogCohortFixture(now)
	if err := ValidateCatalogCohortReceipt(legacy); err != nil {
		t.Fatalf("historical global-publisher receipt became unreadable: %v", err)
	}
	invalid := validCatalogCohortFixture(now)
	for index := range invalid.FenceProofs {
		if invalid.FenceProofs[index].Scope == "mac-project-publishers" {
			invalid.FenceProofs[index].Scope = "project-publishers:qas"
		}
	}
	if err := ValidateCatalogCohortReceipt(invalid); err == nil {
		t.Fatal("historical Dev receipt accepted a publisher proof for another lane")
	}
}

func catalogCohortFenceProofIndex(proofs []CatalogCohortFenceProof, scope string) int {
	for index, proof := range proofs {
		if proof.Scope == scope {
			return index
		}
	}
	return -1
}

func TestCatalogCohortPreflightReportLoaderRequiresCanonicalOwnerOnlyFile(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	_, reports := catalogCohortCreationFixture(t, now)
	report := reports[0]
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(t.TempDir(), "linux-preflight.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCatalogCohortHostPreflightReportFile(path)
	if err != nil || loaded.Preflight.HostID != report.Preflight.HostID {
		t.Fatalf("canonical owner-only preflight report was rejected: host=%q err=%v", loaded.Preflight.HostID, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalogCohortHostPreflightReportFile(path); err == nil {
		t.Fatal("preflight report with broad permissions was accepted")
	}
}

func catalogCohortCreationFixture(t *testing.T, now time.Time) (CatalogReleaseManifest, []CatalogCohortHostPreflightReport) {
	t.Helper()
	bundles := catalogFixtureBundles(t, "cohort-create")
	manifest, err := NewCatalogReleaseManifest("v2.0.9-create", bundles)
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := CatalogReleaseManifestBytes(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	stamp := now.UTC().Format(time.RFC3339Nano)
	reports := make([]CatalogCohortHostPreflightReport, 0, len(catalogCohortSwitchOrder))
	for _, hostID := range catalogCohortSwitchOrder {
		metrics := CatalogWorkInventory{}
		metricsBytes, err := json.Marshal(metrics)
		if err != nil {
			t.Fatal(err)
		}
		metricsDigest := sha256.Sum256(metricsBytes)
		previous := strings.Repeat("3", 64)
		reports = append(reports, CatalogCohortHostPreflightReport{
			Preflight: CatalogCohortHostPreflight{
				HostID: hostID, PreviousTarget: "releases/" + previous, PreviousBundleSHA256: previous, PreviousReadSchemaMax: store.CurrentSchemaVersion,
				Target:              CatalogReleaseIdentity{ReleaseID: manifest.ReleaseID, ReleaseSHA256: hex.EncodeToString(manifestDigest[:]), HostID: hostID, BundleSHA256: catalogManifestBundleSHA256(manifest, hostID)},
				ConfigurationSHA256: strings.Repeat("4", 64), SchemaVersion: store.CurrentSchemaVersion,
				WorkInventorySHA256: hex.EncodeToString(metricsDigest[:]), WorkInventoryEmpty: true, PreflightAt: stamp,
				BackupDirectory: filepath.Join("/private/tmp/catalog-cohort-create", hostID), BackupManifestSHA256: strings.Repeat("6", 64),
				AvailableBytes: 2 << 30, RequiredBytes: 1 << 20,
			},
			WorkInventory: metrics,
		})
	}
	return manifest, reports
}

func TestCatalogCohortReconcileHistoryPreservesDivergentPreSwitchCopies(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	proof := strings.Repeat("a", 64)
	base := validCatalogCohortFixture(now)
	base.OperationID = strings.Repeat("e", 32)
	prepared := base
	held, err := AdvanceCatalogCohortReceipt(base, "hold", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := AdvanceCatalogCohortReceipt(held, "block", "", proof, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	preflighted, err := AdvanceCatalogCohortReceipt(held, "preflight", "linux-host", proof, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	merged, err := ReconcileCatalogCohortReceiptHistories([]CatalogCohortReceipt{preflighted, prepared, blocked, held}, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("reconcile divergent pre-switch copies: %v", err)
	}
	if merged.Stage != CatalogCohortBlocked || blockedCohortResumeStage(merged.Transitions) != CatalogCohortHeld ||
		!catalogCohortStageHoldsAdmissions(merged.Stage) {
		t.Fatalf("reconciled stage=%q resume=%q; all historic holds must remain held", merged.Stage, blockedCohortResumeStage(merged.Transitions))
	}
	if err := ValidateCatalogCohortReceipt(merged); err != nil {
		t.Fatalf("merged receipt invalid: %v", err)
	}
	var reconciliation *CatalogCohortTransition
	for index := range merged.Transitions {
		if merged.Transitions[index].Action == "reconcile-history" {
			reconciliation = &merged.Transitions[index]
		}
	}
	if reconciliation == nil || len(reconciliation.HistoricalBranches) != 4 {
		t.Fatalf("merged receipt did not retain all four histories: %+v", reconciliation)
	}
	for _, branch := range []CatalogCohortReceipt{prepared, held, blocked, preflighted} {
		if !catalogCohortReceiptHasHistoricalBranch(merged, branch.Transitions) || !catalogCohortReceiptUpdateAllowed(branch, merged) {
			t.Fatalf("supported sync would discard historical branch at stage %q", branch.Stage)
		}
	}
	unrelated := validCatalogCohortFixture(now)
	unrelated.OperationID = base.OperationID
	unrelated, err = AdvanceCatalogCohortReceipt(unrelated, "hold", "", strings.Repeat("b", 64), now)
	if err != nil {
		t.Fatal(err)
	}
	if catalogCohortReceiptUpdateAllowed(unrelated, merged) {
		t.Fatal("history reconciliation accepted a local transition chain it did not preserve")
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	var restarted CatalogCohortReceipt
	if err := json.Unmarshal(encoded, &restarted); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCatalogCohortReceipt(restarted); err != nil || !catalogCohortStageHoldsAdmissions(restarted.Stage) {
		t.Fatalf("restarted divergent-history hold lost its evidence: stage=%q err=%v", restarted.Stage, err)
	}
}

func TestCatalogCohortHistoryReconciliationPreservesSwitchesAndRequiresRollback(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	proof := strings.Repeat("c", 64)
	base := validCatalogCohortFixture(now)
	base.OperationID = strings.Repeat("f", 32)
	held, err := AdvanceCatalogCohortReceipt(base, "hold", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	linuxPreflight, err := AdvanceCatalogCohortReceipt(held, "preflight", "linux-host", proof, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	linuxSwitched, err := AdvanceCatalogCohortReceipt(linuxPreflight, "switch", "linux-host", proof, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	sandboxPreflight, err := AdvanceCatalogCohortReceipt(linuxSwitched, "preflight", "sandbox-host", proof, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	switched, err := AdvanceCatalogCohortReceipt(sandboxPreflight, "switch", "sandbox-host", strings.Repeat("d", 64), now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := AdvanceCatalogCohortReceipt(sandboxPreflight, "block", "", strings.Repeat("e", 64), now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	merged, err := ReconcileCatalogCohortReceiptHistories([]CatalogCohortReceipt{blocked, switched}, now.Add(6*time.Second))
	if err != nil {
		t.Fatalf("reconcile a switched host receipt branch: %v", err)
	}
	if merged.Stage != CatalogCohortBlocked || blockedCohortResumeStage(merged.Transitions) != CatalogCohortSwitching ||
		!reflect.DeepEqual(cohortSwitchedHosts(merged), []string{"linux-host", "sandbox-host"}) {
		t.Fatalf("reconciliation stage=%q resume=%q switched=%v", merged.Stage, blockedCohortResumeStage(merged.Transitions), cohortSwitchedHosts(merged))
	}
	if !catalogCohortStageHoldsAdmissions(merged.Stage) || ValidateCatalogCohortReceipt(merged) != nil {
		t.Fatal("reconciled switched history did not retain a valid admission hold")
	}
	if !catalogCohortReceiptHasHistoricalBranch(merged, blocked.Transitions) || !catalogCohortReceiptHasHistoricalBranch(merged, switched.Transitions) {
		t.Fatal("reconciliation did not retain both divergent switched histories")
	}
	currentBytes, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	var afterRestart CatalogCohortReceipt
	if err := json.Unmarshal(currentBytes, &afterRestart); err != nil || ValidateCatalogCohortReceipt(afterRestart) != nil ||
		!catalogCohortStageHoldsAdmissions(afterRestart.Stage) ||
		!catalogCohortReceiptHasHistoricalBranch(afterRestart, switched.Transitions) {
		t.Fatalf("restart during recovery lost the admission hold or switched branch: err=%v", err)
	}
	merged = afterRestart
	merged, err = AdvanceCatalogCohortReceipt(merged, "rollback-start", "", proof, now.Add(7*time.Second))
	if err != nil {
		t.Fatalf("reconciled switched attempt could not enter supported rollback: %v", err)
	}
	merged, err = AdvanceCatalogCohortReceipt(merged, "rollback-host", "sandbox-host", proof, now.Add(8*time.Second))
	if err != nil {
		t.Fatalf("reconciled sandbox switch was not recovered first: %v", err)
	}
	merged, err = AdvanceCatalogCohortReceipt(merged, "rollback-host", "linux-host", proof, now.Add(9*time.Second))
	if err != nil {
		t.Fatalf("reconciled linux switch was not recovered second: %v", err)
	}
	merged, err = AdvanceCatalogCohortReceipt(merged, "rollback-complete", "", proof, now.Add(10*time.Second))
	if err != nil || merged.Stage != CatalogCohortRolledBack {
		t.Fatalf("reconciled rollback stage=%q err=%v", merged.Stage, err)
	}
	if !catalogCohortReceiptSupersessionEligible(merged) {
		t.Fatal("a fully rolled-back reconciled attempt was not eligible for supported supersession")
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	var restarted CatalogCohortReceipt
	if err := json.Unmarshal(encoded, &restarted); err != nil || ValidateCatalogCohortReceipt(restarted) != nil ||
		!catalogCohortReceiptHasHistoricalBranch(restarted, switched.Transitions) {
		t.Fatalf("restart during reconciliation lost the hold or switch history: err=%v", err)
	}

	conflicting := sandboxPreflight
	conflicting, err = AdvanceCatalogCohortReceipt(conflicting, "switch", "sandbox-host", strings.Repeat("f", 64), now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileCatalogCohortReceiptHistories([]CatalogCohortReceipt{switched, conflicting}, now.Add(6*time.Second)); err == nil {
		t.Fatal("reconciliation accepted conflicting selector-switch evidence for the same host")
	}

	inconsistent := held
	inconsistent.FenceProofs = append([]CatalogCohortFenceProof(nil), held.FenceProofs...)
	inconsistent.FenceProofs[0].Control = "different-verified-control"
	if err := ValidateCatalogCohortReceipt(inconsistent); err != nil {
		t.Fatalf("static-identity fixture unexpectedly invalid: %v", err)
	}
	if _, err := ReconcileCatalogCohortReceiptHistories([]CatalogCohortReceipt{held, inconsistent}, now.Add(time.Second)); err == nil {
		t.Fatal("fork reconciliation accepted different publisher-fence identities")
	}
}

func TestCatalogCohortReconciledSwitchRequiresMatchingLocalSelectorJournal(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	receipt, identity := validCatalogCohortForSelectorFixture(t, fixture, now)
	cohortEvidence := strings.Repeat("a", 64)
	var err error
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "hold", "", cohortEvidence, now)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "preflight", "linux-host", cohortEvidence, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	operationID, err := newCatalogOperationID()
	if err != nil {
		t.Fatal(err)
	}
	operation := CatalogSelectorOperation{
		FormatVersion: catalogOperationFormatVersion, OperationID: operationID,
		CohortOperationID: receipt.OperationID, CohortEvidenceSHA256: cohortEvidence,
		HostID: "linux-host", Lane: config.ReleaseLaneDev, Release: identity,
		PreviousTarget: fixture.legacyTarget, NextTarget: filepath.Join(fixture.catalogRoot, fixture.firstRelease, "linux-host"),
		ConfigurationSHA256: receipt.Hosts[0].ConfigurationSHA256, DatabaseSchemaAtSwitch: store.CurrentSchemaVersion,
		BackupDirectory: receipt.Hosts[0].BackupDirectory, BackupManifestSHA256: receipt.Hosts[0].BackupManifestSHA256,
		Stage: catalogOperationComplete, CreatedUTC: now.Add(2 * time.Second).Format(time.RFC3339Nano),
	}
	if err := writeCatalogSelectorOperation(fixture.layout.operationRoot, operation); err != nil {
		t.Fatal(err)
	}
	switchDigest, err := catalogSelectorOperationDigest(fixture.layout, operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "switch", "linux-host", switchDigest, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	status := CatalogSelectorStatus{Kind: "catalog", Target: operation.NextTarget}
	if err := validateCatalogCohortReconciledSelectorEvidence(fixture.layout, receipt, status,
		store.CurrentSchemaVersion, receipt.Hosts[0].ConfigurationSHA256, []CatalogSelectorOperation{operation}); err != nil {
		t.Fatalf("matching local selector journal was rejected: %v", err)
	}
	if err := validateCatalogCohortReconciledSelectorEvidence(fixture.layout, receipt, status,
		store.CurrentSchemaVersion, receipt.Hosts[0].ConfigurationSHA256, nil); err == nil {
		t.Fatal("switched selector without its journal was accepted")
	}
	status.Target = fixture.legacyTarget
	if err := validateCatalogCohortReconciledSelectorEvidence(fixture.layout, receipt, status,
		store.CurrentSchemaVersion, receipt.Hosts[0].ConfigurationSHA256, []CatalogSelectorOperation{operation}); err == nil {
		t.Fatal("selector target that disagrees with the switch journal was accepted")
	}
}

func TestCatalogCohortSupersessionRequiresRecoveryForAnyUnrolledBackSwitch(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	proof := strings.Repeat("a", 64)
	old := validCatalogCohortFixture(now)
	var err error
	old, err = AdvanceCatalogCohortReceipt(old, "hold", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	old, err = AdvanceCatalogCohortReceipt(old, "preflight", "linux-host", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	old, err = AdvanceCatalogCohortReceipt(old, "switch", "linux-host", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	old, err = AdvanceCatalogCohortReceipt(old, "block", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	successor := heldCatalogCohortFixture(t, now, strings.Repeat("b", 32), "v2.0.1")
	observation := fixtureCohortSupersessionEvidence(old, successor, 0, "none")
	if _, err := AdvanceCatalogCohortSupersedeHost(old, successor, observation, now); err == nil {
		t.Fatal("a switched deployment with no rollback was superseded instead of requiring recovery")
	}
	if !catalogCohortStageHoldsAdmissions(old.Stage) {
		t.Fatal("interrupted switched deployment lost its admission hold")
	}
	if err := ValidateCatalogCohortReceipt(old); err != nil {
		t.Fatalf("interrupted receipt history became unreadable: %v", err)
	}

	// A completed supported rollback is eligible for supersession after every
	// local selector journal is recorded as rolled back.
	old, err = AdvanceCatalogCohortReceipt(old, "rollback-start", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	old, err = AdvanceCatalogCohortReceipt(old, "rollback-host", "linux-host", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	old, err = AdvanceCatalogCohortReceipt(old, "rollback-complete", "", proof, now)
	if err != nil || old.Stage != CatalogCohortRolledBack {
		t.Fatalf("completed rollback stage=%q err=%v", old.Stage, err)
	}
	for hostIndex, hostID := range catalogCohortSwitchOrder {
		disposition := "none"
		if hostID == "linux-host" {
			disposition = "rolled_back"
		}
		observation := fixtureCohortSupersessionEvidence(old, successor, hostIndex, disposition)
		observation.HostID = hostID
		old, err = AdvanceCatalogCohortSupersedeHost(old, successor, observation, now)
		if err != nil {
			t.Fatalf("rolled-back attempt host %s supersede observation: %v", hostID, err)
		}
	}
	old, err = AdvanceCatalogCohortSupersede(old, successor.OperationID, now)
	if err != nil || old.Stage != CatalogCohortSuperseded {
		t.Fatalf("fully rolled-back attempt was not retired: stage=%q err=%v", old.Stage, err)
	}
}

func TestCatalogCohortCompletedReleaseCanBeSupersededWithExactBundleEvidence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	proof := strings.Repeat("a", 64)
	old := validCatalogCohortFixture(now)
	var err error
	old, err = AdvanceCatalogCohortReceipt(old, "hold", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, hostID := range catalogCohortSwitchOrder {
		old, err = AdvanceCatalogCohortReceipt(old, "preflight", hostID, proof, now)
		if err != nil {
			t.Fatalf("preflight %s: %v", hostID, err)
		}
		old, err = AdvanceCatalogCohortReceipt(old, "switch", hostID, proof, now)
		if err != nil {
			t.Fatalf("switch %s: %v", hostID, err)
		}
	}
	old, err = AdvanceCatalogCohortReceipt(old, "verify-start", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, hostID := range catalogCohortSwitchOrder {
		old, err = AdvanceCatalogCohortReceipt(old, "verify", hostID, proof, now)
		if err != nil {
			t.Fatalf("verify %s: %v", hostID, err)
		}
	}
	old, err = AdvanceCatalogCohortReceipt(old, "complete", "", proof, now)
	if err != nil || old.Stage != CatalogCohortComplete {
		t.Fatalf("completed predecessor stage=%q err=%v", old.Stage, err)
	}

	successor := heldCatalogCohortFixture(t, now, strings.Repeat("b", 32), "v2.0.1")
	for index, hostID := range catalogCohortSwitchOrder {
		plan, err := BuildCatalogPlan(hostID, config.ReleaseLaneDev)
		if err != nil {
			t.Fatal(err)
		}
		catalogRoot, err := DefaultCatalogRootForPlan(plan)
		if err != nil {
			t.Fatal(err)
		}
		successor.Hosts[index].PreviousTarget = filepath.Join(catalogRoot, old.ReleaseID, hostID)
		successor.Hosts[index].PreviousBundleSHA256 = old.Hosts[index].Target.BundleSHA256
	}
	if err := ValidateCatalogCohortReceipt(successor); err != nil {
		t.Fatalf("successor fixture invalid: %v", err)
	}

	wrong := fixtureCohortSupersessionEvidence(old, successor, 0, "completed")
	wrong.SelectorBundleSHA256 = strings.Repeat("f", 64)
	if _, err := AdvanceCatalogCohortSupersedeHost(old, successor, wrong, now); err == nil {
		t.Fatal("completed release with a different selected bundle identity was superseded")
	}
	wrong = fixtureCohortSupersessionEvidence(old, successor, 0, "completed")
	wrong.SelectorBundleSHA256 = old.Hosts[0].Target.BundleSHA256
	successor.Hosts[0].PreviousBundleSHA256 = strings.Repeat("f", 64)
	if _, err := AdvanceCatalogCohortSupersedeHost(old, successor, wrong, now); err == nil {
		t.Fatal("successor with a mismatched previous bundle identity was accepted")
	}
	successor.Hosts[0].PreviousBundleSHA256 = old.Hosts[0].Target.BundleSHA256

	for index, hostID := range catalogCohortSwitchOrder {
		observation := fixtureCohortSupersessionEvidence(old, successor, index, "completed")
		observation.HostID = hostID
		observation.SelectorBundleSHA256 = old.Hosts[index].Target.BundleSHA256
		old, err = AdvanceCatalogCohortSupersedeHost(old, successor, observation, now)
		if err != nil {
			t.Fatalf("completed predecessor host %s supersession: %v", hostID, err)
		}
		if index < len(catalogCohortSwitchOrder)-1 && !catalogCohortStageHoldsAdmissions(old.Stage) {
			t.Fatalf("completed predecessor released admissions after only %d host observations", index+1)
		}
	}
	old, err = AdvanceCatalogCohortSupersede(old, successor.OperationID, now)
	if err != nil || old.Stage != CatalogCohortSuperseded || catalogCohortStageHoldsAdmissions(old.Stage) {
		t.Fatalf("completed predecessor was not safely retired: stage=%q err=%v", old.Stage, err)
	}
	if err := ValidateCatalogCohortReceipt(old); err != nil {
		t.Fatalf("superseded history failed validation: %v", err)
	}
}

func TestCatalogCohortCompletedSupersessionRequiresMatchingLocalSelectorJournal(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	old := validCatalogCohortFixture(now)
	old.ReleaseID = fixture.firstRelease
	status, err := InspectCatalogRelease(fixture.catalogRoot, fixture.layout.hostID, fixture.firstRelease)
	if err != nil {
		t.Fatal(err)
	}
	old.ReleaseSHA256 = status.Identity.ReleaseSHA256
	for index := range old.Hosts {
		old.Hosts[index].Target.ReleaseID = old.ReleaseID
		old.Hosts[index].Target.ReleaseSHA256 = old.ReleaseSHA256
	}
	old.Hosts[0].Target = status.Identity
	old.Hosts[0].PreviousTarget = fixture.legacyTarget
	old.Hosts[0].PreviousBundleSHA256 = filepath.Base(fixture.legacyTarget)
	configurationSHA256, err := hashCatalogConfiguration(fixture.layout.configFile)
	if err != nil {
		t.Fatal(err)
	}
	old.Hosts[0].ConfigurationSHA256 = configurationSHA256
	backup := fixtureCatalogBackup(t, fixture.layout, fixture.legacyTarget, store.CurrentSchemaVersion)
	old.Hosts[0].BackupDirectory = backup.Directory
	old.Hosts[0].BackupManifestSHA256 = backup.ManifestSHA256
	if err := ValidateCatalogCohortReceipt(old); err != nil {
		t.Fatalf("completed receipt fixture invalid: %v", err)
	}

	proof := strings.Repeat("a", 64)
	old, err = AdvanceCatalogCohortReceipt(old, "hold", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	old, err = AdvanceCatalogCohortReceipt(old, "preflight", "linux-host", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	backup.CohortOperationID = old.OperationID
	backup.CohortEvidenceSHA256 = proof
	operation, err := selectCatalogReleaseLocked(fixture.layout, fixture.firstRelease, fixture.legacyTarget, store.CurrentSchemaVersion, backup, nil)
	if err != nil {
		t.Fatalf("fixture selector switch: %v", err)
	}
	operationDigest, err := catalogSelectorOperationDigest(fixture.layout, operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	old, err = AdvanceCatalogCohortReceipt(old, "switch", "linux-host", operationDigest, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, hostID := range catalogCohortSwitchOrder[1:] {
		old, err = AdvanceCatalogCohortReceipt(old, "preflight", hostID, proof, now)
		if err != nil {
			t.Fatal(err)
		}
		old, err = AdvanceCatalogCohortReceipt(old, "switch", hostID, proof, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	old, err = AdvanceCatalogCohortReceipt(old, "verify-start", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, hostID := range catalogCohortSwitchOrder {
		old, err = AdvanceCatalogCohortReceipt(old, "verify", hostID, proof, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	old, err = AdvanceCatalogCohortReceipt(old, "complete", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	if disposition, err := catalogCohortSelectorOperationDisposition(fixture.layout, old); err != nil || disposition != "completed" {
		t.Fatalf("matching completed journal disposition=%q err=%v", disposition, err)
	}
	if err := os.Remove(filepath.Join(fixture.layout.serviceRoot, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fixture.legacyTarget, filepath.Join(fixture.layout.serviceRoot, "current")); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogCohortSelectorOperationDisposition(fixture.layout, old); err == nil {
		t.Fatal("completed receipt was accepted after the current selector diverged from its journal")
	}
}

func TestCatalogCohortSupersessionRejectsUnfinishedSelectorJournal(t *testing.T) {
	fixture := newCatalogSelectorFixture(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	old := validCatalogCohortFixture(time.Now().UTC().Truncate(time.Second))
	operationID, err := newCatalogOperationID()
	if err != nil {
		t.Fatal(err)
	}
	operation := CatalogSelectorOperation{
		FormatVersion: catalogOperationFormatVersion, OperationID: operationID,
		CohortOperationID: old.OperationID, CohortEvidenceSHA256: strings.Repeat("a", 64),
		HostID: "linux-host", Lane: config.ReleaseLaneDev,
		Release:        CatalogReleaseIdentity{ReleaseID: fixture.firstRelease, ReleaseSHA256: strings.Repeat("b", 64), HostID: "linux-host", BundleSHA256: strings.Repeat("c", 64)},
		PreviousTarget: fixture.legacyTarget, NextTarget: filepath.Join(fixture.catalogRoot, fixture.firstRelease, "linux-host"),
		ConfigurationSHA256: strings.Repeat("d", 64), DatabaseSchemaAtSwitch: store.CurrentSchemaVersion,
		BackupDirectory: filepath.Join(fixture.layout.backupRoot, ".simple-backup-"+operationID), BackupManifestSHA256: strings.Repeat("e", 64),
		Stage: catalogOperationPrepared, CreatedUTC: now,
	}
	if err := writeCatalogSelectorOperation(fixture.layout.operationRoot, operation); err != nil {
		t.Fatal(err)
	}
	if _, err := catalogCohortSelectorOperationDisposition(fixture.layout, old); err == nil {
		t.Fatal("abandoned receipt ignored an unfinished host selector operation")
	}
}

func TestCatalogCohortSupersessionRestartKeepsHoldUntilAllHostsReconcile(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	proof := strings.Repeat("e", 64)
	old := validCatalogCohortFixture(now)
	var err error
	old, err = AdvanceCatalogCohortReceipt(old, "hold", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	successor := heldCatalogCohortFixture(t, now, strings.Repeat("d", 32), "v2.0.2")
	for hostIndex, hostID := range catalogCohortSwitchOrder[:2] {
		observation := fixtureCohortSupersessionEvidence(old, successor, hostIndex, "none")
		observation.HostID = hostID
		old, err = AdvanceCatalogCohortSupersedeHost(old, successor, observation, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	unsafe := fixtureCohortSupersessionEvidence(old, successor, 2, "none")
	unsafe.HostID = "factory-sandbox"
	unsafe.ServiceActive = true
	if _, err := AdvanceCatalogCohortSupersedeHost(old, successor, unsafe, now); err == nil {
		t.Fatal("supersession accepted an active service observation")
	}
	unsafe = fixtureCohortSupersessionEvidence(old, successor, 2, "none")
	unsafe.HostID = "factory-sandbox"
	unsafe.WorkInventoryEmpty = false
	if _, err := AdvanceCatalogCohortSupersedeHost(old, successor, unsafe, now); err == nil {
		t.Fatal("supersession accepted retained work")
	}
	unsafe = fixtureCohortSupersessionEvidence(old, successor, 2, "prepared")
	unsafe.HostID = "factory-sandbox"
	if _, err := AdvanceCatalogCohortSupersedeHost(old, successor, unsafe, now); err == nil {
		t.Fatal("supersession accepted an unreconciled selector operation")
	}
	if old.Stage != CatalogCohortHeld || !catalogCohortStageHoldsAdmissions(old.Stage) || len(old.Transitions) != 3 {
		t.Fatalf("partial reconciliation stage=%q transitions=%d; old hold must persist", old.Stage, len(old.Transitions))
	}
	encoded, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	var restarted CatalogCohortReceipt
	if err := json.Unmarshal(encoded, &restarted); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCatalogCohortReceipt(restarted); err != nil {
		t.Fatalf("receipt could not resume after process restart: %v", err)
	}
	if restarted.Transitions[1].SHA256 != old.Transitions[1].SHA256 || restarted.Transitions[2].SHA256 != old.Transitions[2].SHA256 {
		t.Fatal("restart changed preserved historical reconciliation evidence")
	}
	for hostIndex, hostID := range catalogCohortSwitchOrder[2:] {
		observation := fixtureCohortSupersessionEvidence(restarted, successor, hostIndex+2, "none")
		observation.HostID = hostID
		restarted, err = AdvanceCatalogCohortSupersedeHost(restarted, successor, observation, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !catalogCohortStageHoldsAdmissions(restarted.Stage) {
		t.Fatal("host observations alone released the old hold before finalization")
	}
	restarted, err = AdvanceCatalogCohortSupersede(restarted, successor.OperationID, now)
	if err != nil || restarted.Stage != CatalogCohortSuperseded || catalogCohortStageHoldsAdmissions(restarted.Stage) {
		t.Fatalf("post-restart finalization stage=%q err=%v", restarted.Stage, err)
	}
}

func TestCatalogCohortSupersessionCanRestartAfterSuccessorEvidenceExpires(t *testing.T) {
	started := time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	proof := strings.Repeat("e", 64)
	old := validCatalogCohortFixture(started)
	var err error
	old, err = AdvanceCatalogCohortReceipt(old, "hold", "", proof, started)
	if err != nil {
		t.Fatal(err)
	}
	old, err = AdvanceCatalogCohortReceipt(old, "block", "", proof, started.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	firstSuccessor := heldCatalogCohortFixture(t, started, strings.Repeat("d", 32), "v2.0.2")
	firstObservation := fixtureCohortSupersessionEvidence(old, firstSuccessor, 0, "none")
	old, err = AdvanceCatalogCohortSupersedeHost(old, firstSuccessor, firstObservation, started.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	firstEvidenceSHA := old.Transitions[len(old.Transitions)-1].SHA256

	tooEarlyAt := started.Add(3 * time.Minute)
	tooEarlySuccessor := heldCatalogCohortFixture(t, tooEarlyAt, strings.Repeat("f", 32), "v2.0.3")
	if _, err := AdvanceCatalogCohortSupersessionRestart(old, firstSuccessor, tooEarlySuccessor, tooEarlyAt); err == nil {
		t.Fatal("supersession restarted while the previous evidence was still fresh")
	}

	restartAt := started.Add(11 * time.Minute)
	nextSuccessor := heldCatalogCohortFixture(t, restartAt, strings.Repeat("a", 32), "v2.0.4")
	old, err = AdvanceCatalogCohortSupersessionRestart(old, firstSuccessor, nextSuccessor, restartAt)
	if err != nil {
		t.Fatal(err)
	}
	if old.Stage != CatalogCohortBlocked || !catalogCohortStageHoldsAdmissions(old.Stage) ||
		old.Transitions[2].SHA256 != firstEvidenceSHA || old.Transitions[3].Action != "supersession-restart" {
		t.Fatalf("restart stage=%q transitions=%d; hold and prior evidence must remain", old.Stage, len(old.Transitions))
	}
	encoded, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	var resumed CatalogCohortReceipt
	if err := json.Unmarshal(encoded, &resumed); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCatalogCohortReceipt(resumed); err != nil {
		t.Fatalf("restarted supersession receipt failed validation: %v", err)
	}
	for hostIndex, hostID := range catalogCohortSwitchOrder {
		observation := fixtureCohortSupersessionEvidence(resumed, nextSuccessor, hostIndex, "none")
		observation.HostID = hostID
		resumed, err = AdvanceCatalogCohortSupersedeHost(resumed, nextSuccessor, observation, restartAt)
		if err != nil {
			t.Fatalf("fresh successor host %s: %v", hostID, err)
		}
	}
	resumed, err = AdvanceCatalogCohortSupersede(resumed, nextSuccessor.OperationID, restartAt)
	if err != nil || resumed.Stage != CatalogCohortSuperseded {
		t.Fatalf("fresh supersession stage=%q err=%v", resumed.Stage, err)
	}
}

func heldCatalogCohortFixture(t *testing.T, now time.Time, operationID, releaseID string) CatalogCohortReceipt {
	t.Helper()
	receipt := validCatalogCohortFixture(now)
	receipt.OperationID = operationID
	receipt.ReleaseID = releaseID
	receipt.ReleaseSHA256 = strings.Repeat("c", 64)
	for index := range receipt.Hosts {
		receipt.Hosts[index].Target.ReleaseID = releaseID
		receipt.Hosts[index].Target.ReleaseSHA256 = receipt.ReleaseSHA256
	}
	var err error
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "hold", "", strings.Repeat("9", 64), now)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func fixtureCohortSupersessionEvidence(old, successor CatalogCohortReceipt, hostIndex int, operations string) CatalogCohortSupersessionEvidence {
	selectorBundleSHA256 := old.Hosts[hostIndex].PreviousBundleSHA256
	if operations == "completed" {
		selectorBundleSHA256 = old.Hosts[hostIndex].Target.BundleSHA256
	}
	return CatalogCohortSupersessionEvidence{
		HostID: successor.Hosts[hostIndex].HostID, SuccessorOperationID: successor.OperationID,
		ExpectedSelector: successor.Hosts[hostIndex].PreviousTarget, ObservedSelector: successor.Hosts[hostIndex].PreviousTarget,
		ServiceActive: false, WorkInventorySHA256: successor.Hosts[hostIndex].WorkInventorySHA256, WorkInventoryEmpty: true,
		ConfigurationSHA256: successor.Hosts[hostIndex].ConfigurationSHA256, DatabaseSchema: successor.Hosts[hostIndex].SchemaVersion,
		SelectorBundleSHA256: selectorBundleSHA256, SelectorOperations: operations,
	}
}

func writeCohortReceiptFixture(t *testing.T, root string, receipt CatalogCohortReceipt) {
	t.Helper()
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, receipt.OperationID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeLegacyCohortReceiptFixture(t *testing.T, root string, receipt CatalogCohortReceipt) {
	t.Helper()
	data, err := marshalCatalogCohortReceiptWithoutServiceActiveField(receipt)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, receipt.OperationID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
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
		store.CurrentSchemaVersion, host.BackupDirectory, host.BackupManifestSHA256, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("fresh cohort backup was rejected before the local two-minute switch preflight: %v", err)
	}
	refreshed, err := AdvanceCatalogCohortReceipt(receipt, "preflight", "linux-host", proof, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("fresh local preflight was rejected against the still-valid cohort backup: %v", err)
	}
	if _, err := AdvanceCatalogCohortReceipt(refreshed, "switch", "linux-host", proof, now.Add(3*time.Minute+time.Second)); err != nil {
		t.Fatalf("switch did not accept the recent local preflight: %v", err)
	}
	if err := validateCatalogCohortHostMaySwitch(receipt, fixture.layout, fixture.legacyTarget, target,
		store.CurrentSchemaVersion, host.BackupDirectory, host.BackupManifestSHA256, now.Add(11*time.Minute)); err == nil {
		t.Fatal("expired cohort backup/preflight was accepted")
	}
	if _, err := AdvanceCatalogCohortReceipt(receipt, "switch", "sandbox-host", proof, now); err == nil {
		t.Fatal("cohort receipt accepted a host that skipped the prescribed order")
	}
}

func TestCatalogPreSwitchCheckRequiresOneExactAdmissionHold(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	plan, err := BuildCatalogPlan("linux-host", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "catalog-cohorts")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	first := validCatalogCohortFixture(now)
	first.OperationID = strings.Repeat("a", 32)
	first, err = AdvanceCatalogCohortReceipt(first, "hold", "", strings.Repeat("b", 64), now)
	if err != nil {
		t.Fatalf("hold first cohort: %v", err)
	}
	writeCohortReceiptFixture(t, root, first)
	if err := catalogCohortAdmissionHoldMatchesAt(plan, root, first.OperationID); err != nil {
		t.Fatalf("exact single held operation was rejected: %v", err)
	}
	if err := catalogCohortAdmissionHoldMatchesAt(plan, root, strings.Repeat("c", 32)); err == nil {
		t.Fatal("wrong cohort operation passed the pre-switch check")
	}

	second := validCatalogCohortFixture(now)
	second.OperationID = strings.Repeat("d", 32)
	second, err = AdvanceCatalogCohortReceipt(second, "hold", "", strings.Repeat("e", 64), now)
	if err != nil {
		t.Fatalf("hold second cohort: %v", err)
	}
	writeCohortReceiptFixture(t, root, second)
	if _, _, err := catalogCohortAdmissionHeldAt(plan, root); err == nil {
		t.Fatal("two simultaneous holds did not fail closed")
	}
	if err := catalogCohortAdmissionHoldMatchesAt(plan, root, first.OperationID); err == nil {
		t.Fatal("ambiguous receipts passed the exact pre-switch check")
	}
}

func TestCatalogCohortDelayedReconciliationMayCloseRecordedSwitchButNewSwitchStaysFresh(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	proof := strings.Repeat("c", 64)
	receipt := validCatalogCohortFixture(now)
	var err error
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "hold", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "preflight", "linux-host", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	reconciledAt := now.Add(4 * time.Minute)
	if _, err := AdvanceCatalogCohortReceipt(receipt, "switch", "linux-host", proof, reconciledAt); err == nil {
		t.Fatal("new switch accepted a preflight older than two minutes")
	}
	if !catalogCohortSwitchRecordTimingValid(now, reconciledAt, true) {
		t.Fatal("delayed reconciliation of a recorded selector switch was rejected")
	}
	if catalogCohortSwitchRecordTimingValid(now, reconciledAt, false) {
		t.Fatal("new selector switch bypassed the two-minute preflight window")
	}
	if catalogCohortSwitchRecordTimingValid(reconciledAt, now, true) {
		t.Fatal("reconciliation accepted a selector operation created before its preflight")
	}
	if _, err := AdvanceCatalogCohortReceipt(receipt, "switch-reconciled", "linux-host", proof, reconciledAt); err != nil {
		t.Fatalf("hash-linked recovery event did not preserve the original hold and preflight history: %v", err)
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
