package laneinstall

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/store"
)

const catalogCohortContinuationOperationID = "c2f074a84bc864830266a209943319eb"
const catalogCohortContinuationReleaseID = "cat-20261022-7a42f3b"

func TestCatalogCohortContinuationRenewalKeepsReceiptSeparateAndBounded(t *testing.T) {
	started := time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC)
	receipt := expiredPRDSwitchingReceiptFixture(t, started)
	observedAt := started.Add(61*time.Minute + 5*time.Second)
	observations := continuationObservationFixtures(t, receipt, observedAt)
	verifiedAt := observedAt.Add(10 * time.Second)
	renewal, err := NewCatalogCohortContinuationRenewal(receipt, observations, verifiedAt)
	if err != nil {
		t.Fatalf("create operation-specific continuation: %v", err)
	}
	if renewal.OperationID != receipt.OperationID || renewal.ExpiresAt != verifiedAt.Add(4*time.Hour).Format(time.RFC3339Nano) {
		t.Fatalf("renewal identity/window mismatch: %+v", renewal)
	}
	boundary := verifiedAt.Add(CatalogCohortContinuationWindow)
	if err := ValidateCatalogCohortContinuationRenewalForReceipt(renewal, receipt, boundary); err != nil {
		t.Fatalf("renewal rejected at inclusive four-hour boundary: %v", err)
	}
	if err := ValidateCatalogCohortContinuationRenewalForReceipt(renewal, receipt, boundary.Add(time.Nanosecond)); err == nil {
		t.Fatal("renewal accepted after its four-hour expiry")
	}

	staging := catalogTestTempDir(t)
	receiptRoot := filepath.Join(staging, "catalog-cohorts")
	if _, err := writeCatalogCohortReceiptAt(receiptRoot, receipt); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(staging, "catalog-cohort-continuations", receipt.OperationID+".json")
	dataBefore, err := os.ReadFile(filepath.Join(receiptRoot, receipt.OperationID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	canonicalRenewal, err := canonicalJSON(renewal)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCanonicalOwnerJSONNoReplace(path, canonicalRenewal); err != nil {
		t.Fatalf("write separate renewal evidence: %v", err)
	}
	loaded, err := readCatalogCohortReceipt(filepath.Join(receiptRoot, receipt.OperationID+".json"))
	if err != nil {
		t.Fatalf("unchanged receipt parser rejected receipt after separate evidence write: %v", err)
	}
	dataAfter, err := os.ReadFile(filepath.Join(receiptRoot, receipt.OperationID+".json"))
	if err != nil || !bytes.Equal(dataBefore, dataAfter) || loaded.UpdatedAt != receipt.UpdatedAt ||
		lastCohortTransitionDigest(loaded.Transitions) != lastCohortTransitionDigest(receipt.Transitions) {
		t.Fatal("renewal changed the receipt bytes, transition history, or timestamps")
	}
	if _, err := ReadCatalogCohortContinuationRenewal(
		Plan{HostID: "linux-host", Lane: config.ReleaseLanePRD, Namespace: config.LaneNamespace{Staging: staging}}, receipt.OperationID); err == nil {
		t.Fatal("renewal reader accepted an arbitrary namespace that is not a canonical host plan")
	}
}

func TestCatalogCohortContinuationRenewalRequiresFreshConsistentHostProofs(t *testing.T) {
	started := time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC)
	receipt := expiredPRDSwitchingReceiptFixture(t, started)
	observedAt := started.Add(61*time.Minute + 5*time.Second)
	observations := continuationObservationFixtures(t, receipt, observedAt)
	verifiedAt := observedAt.Add(10 * time.Second)

	mutated := append([]CatalogCohortContinuationHostObservation(nil), observations...)
	mutated[1].WorkInventoryEmpty = false
	if _, err := NewCatalogCohortContinuationRenewal(receipt, mutated, verifiedAt); err == nil {
		t.Fatal("renewal accepted a nonempty Sandbox work inventory")
	}
	mutated = append([]CatalogCohortContinuationHostObservation(nil), observations...)
	mutated[2].BaseTransitionSHA256 = strings.Repeat("f", 64)
	if _, err := NewCatalogCohortContinuationRenewal(receipt, mutated, verifiedAt); err == nil {
		t.Fatal("renewal accepted a Factory report from another receipt history")
	}
	mutated = append([]CatalogCohortContinuationHostObservation(nil), observations...)
	mutated[0].SelectorJournalSHA256 = strings.Repeat("f", 64)
	if _, err := NewCatalogCohortContinuationRenewal(receipt, mutated, verifiedAt); err == nil {
		t.Fatal("renewal accepted a Linux selector journal that does not match the receipt")
	}
	mutated = append([]CatalogCohortContinuationHostObservation(nil), observations...)
	mutated[3].ObservedAt = verifiedAt.Add(-3 * time.Minute).Format(time.RFC3339Nano)
	if _, err := NewCatalogCohortContinuationRenewal(receipt, mutated, verifiedAt); err == nil {
		t.Fatal("renewal accepted a host observation older than two minutes")
	}
	mutated = append([]CatalogCohortContinuationHostObservation(nil), observations...)
	mutated[3].ObservedAt = verifiedAt.Add(time.Nanosecond).Format(time.RFC3339Nano)
	if _, err := NewCatalogCohortContinuationRenewal(receipt, mutated, verifiedAt); err == nil {
		t.Fatal("renewal accepted a host observation after the coordinator verification time")
	}
	if _, err := NewCatalogCohortContinuationRenewal(receipt, observations, started.Add(59*time.Minute)); err == nil {
		t.Fatal("renewal started before the original cohort evidence expired")
	}
}

func TestCatalogCohortContinuationIsGenericAcrossLanesAndHeldState(t *testing.T) {
	started := time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC)
	receipt := validCatalogCohortFixture(started)
	receipt, err := AdvanceCatalogCohortReceipt(receipt, "hold", "", strings.Repeat("a", 64), started.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Lane != config.ReleaseLaneDev || receipt.Stage != CatalogCohortHeld || !catalogCohortContinuationEligible(receipt) {
		t.Fatalf("Dev held cohort is not continuation eligible: lane=%s stage=%s", receipt.Lane, receipt.Stage)
	}
	observedAt := started.Add(61*time.Minute + 5*time.Second)
	observations := make([]CatalogCohortContinuationHostObservation, 0, len(catalogCohortSwitchOrder))
	for index, hostID := range catalogCohortSwitchOrder {
		host := receipt.Hosts[index]
		kind := "catalog"
		if hostID == "mac-control-plane" {
			kind = "legacy"
		}
		observations = append(observations, CatalogCohortContinuationHostObservation{
			FormatVersion: catalogCohortContinuationFormatVersion, OperationID: receipt.OperationID, HostID: hostID,
			Lane: receipt.Lane, ReleaseID: receipt.ReleaseID, ReleaseSHA256: receipt.ReleaseSHA256,
			BaseTransitionCount: len(receipt.Transitions), BaseTransitionSHA256: lastCohortTransitionDigest(receipt.Transitions),
			SelectorKind: kind, SelectorTarget: host.PreviousTarget, SelectorBundleSHA256: host.PreviousBundleSHA256,
			ConfigurationSHA256: host.ConfigurationSHA256, DatabaseSchema: host.SchemaVersion,
			WorkInventorySHA256: host.WorkInventorySHA256, WorkInventoryEmpty: true,
			BackupDirectory: host.BackupDirectory, BackupManifestSHA256: host.BackupManifestSHA256,
			AvailableBytes: 2 << 30, RequiredBytes: host.RequiredBytes, ServiceStopped: true,
			AdmissionHeld: true, AdmissionOperationID: receipt.OperationID, ObservedAt: observedAt.Format(time.RFC3339Nano),
		})
	}
	verifiedAt := observedAt.Add(10 * time.Second)
	for _, observation := range observations {
		if err := validateCatalogCohortContinuationHostObservation(observation, receipt); err != nil {
			t.Fatalf("validate generic observation for %s: %v; %+v", observation.HostID, err, observation)
		}
	}
	renewal, err := NewCatalogCohortContinuationRenewal(receipt, observations, verifiedAt)
	if err != nil {
		t.Fatalf("create generic Dev continuation: %v", err)
	}
	if renewal.Lane != config.ReleaseLaneDev || renewal.OperationID != receipt.OperationID {
		t.Fatalf("renewal lost lane or operation binding: %+v", renewal)
	}
	if err := ValidateCatalogCohortContinuationRenewalForReceipt(renewal, receipt, verifiedAt.Add(CatalogCohortContinuationWindow)); err != nil {
		t.Fatalf("generic renewal rejected at its inclusive expiry: %v", err)
	}
	if err := ValidateCatalogCohortContinuationRenewalForReceipt(renewal, receipt, verifiedAt.Add(CatalogCohortContinuationWindow+time.Nanosecond)); err == nil {
		t.Fatal("generic renewal exceeded its bounded four-hour continuation")
	}
	mutated := renewal
	mutated.Lane = config.ReleaseLaneQAS
	mutated.SHA256 = catalogCohortContinuationDigest(mutated)
	if ValidateCatalogCohortContinuationRenewalForReceipt(mutated, receipt, verifiedAt) == nil {
		t.Fatal("renewal was accepted after changing its selected lane")
	}
}

func TestCatalogCohortContinuationRejectsIllegalPrefixAndTerminalStages(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	receipt := validCatalogCohortFixture(now)
	if catalogCohortContinuationEligible(receipt) {
		t.Fatal("prepared receipt must receive its supported hold before continuation")
	}
	held, err := AdvanceCatalogCohortReceipt(receipt, "hold", "", strings.Repeat("a", 64), now)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := AdvanceCatalogCohortReceipt(held, "block", "", strings.Repeat("b", 64), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if catalogCohortContinuationEligible(blocked) {
		t.Fatal("blocked receipt must be reconciled through supported controls before continuation")
	}
	complete := held
	for _, hostID := range catalogCohortSwitchOrder {
		complete, err = AdvanceCatalogCohortReceipt(complete, "preflight", hostID, strings.Repeat("c", 64), now.Add(time.Duration(len(complete.Transitions)+1)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		complete, err = AdvanceCatalogCohortReceipt(complete, "switch", hostID, strings.Repeat("d", 64), now.Add(time.Duration(len(complete.Transitions)+1)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	complete, err = AdvanceCatalogCohortReceipt(complete, "verify-start", "", strings.Repeat("e", 64), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !catalogCohortContinuationEligible(complete) {
		t.Fatal("fully switched verifying receipt should be continuation eligible")
	}
	wrongOrder := complete
	wrongOrder.Stage = CatalogCohortSwitching
	if catalogCohortContinuationEligible(wrongOrder) {
		t.Fatal("receipt with a stage inconsistent with its transition history was accepted")
	}
}

func TestCatalogCohortContinuationAllowsExpiredCohortButKeepsTwoMinuteHostCheck(t *testing.T) {
	started := time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC)
	fixture := newCatalogSelectorFixture(t)
	fixture.layout.lane = config.ReleaseLanePRD
	configSHA, err := hashCatalogConfiguration(fixture.layout.configFile)
	if err != nil {
		t.Fatal(err)
	}
	receipt := expiredPRDSwitchingReceiptFixtureWithConfig(t, started, map[string]string{"sandbox-host": configSHA})
	sandboxIndex := cohortHostIndex(receipt, "sandbox-host")
	observedAt := started.Add(61*time.Minute + 5*time.Second)
	observations := continuationObservationFixtures(t, receipt, observedAt)
	verifiedAt := observedAt.Add(10 * time.Second)
	renewal, err := NewCatalogCohortContinuationRenewal(receipt, observations, verifiedAt)
	if err != nil {
		t.Fatalf("create renewal fixture: %v", err)
	}
	preflightAt := verifiedAt.Add(time.Minute)
	proof := strings.Repeat("b", 64)
	updated, err := AdvanceCatalogCohortReceipt(receipt, "preflight", "sandbox-host", proof, preflightAt)
	if err != nil {
		t.Fatalf("append a new local preflight without rewriting older timestamps: %v", err)
	}
	if err := ValidateCatalogCohortContinuationRenewalForReceipt(renewal, updated, preflightAt); err != nil {
		t.Fatalf("renewal no longer matches its original receipt prefix: %v", err)
	}
	root := filepath.Dir(fixture.layout.operationRoot)
	renewalRoot := filepath.Join(root, "catalog-cohort-continuations")
	if err := os.MkdirAll(renewalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(renewalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := canonicalJSON(renewal)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCanonicalOwnerJSONNoReplace(filepath.Join(renewalRoot, receipt.OperationID+".json"), data); err != nil {
		t.Fatal(err)
	}
	host := updated.Hosts[sandboxIndex]
	target := host.Target
	layout := fixture.layout
	layout.hostID = "sandbox-host"
	nowAtBoundary := preflightAt.Add(2 * time.Minute)
	if err := validateCatalogCohortHostMaySwitch(updated, layout, host.PreviousTarget, target,
		host.SchemaVersion, host.BackupDirectory, host.BackupManifestSHA256, nowAtBoundary); err != nil {
		t.Fatalf("renewed cohort rejected exact two-minute local preflight boundary: %v", err)
	}
	if err := validateCatalogCohortHostMaySwitch(updated, layout, host.PreviousTarget, target,
		host.SchemaVersion, host.BackupDirectory, host.BackupManifestSHA256, nowAtBoundary.Add(time.Nanosecond)); err == nil {
		t.Fatal("renewed cohort accepted a local preflight one nanosecond past two minutes")
	}
}

func expiredPRDSwitchingReceiptFixture(t *testing.T, started time.Time) CatalogCohortReceipt {
	return expiredPRDSwitchingReceiptFixtureWithConfig(t, started, nil)
}

func expiredPRDSwitchingReceiptFixtureWithConfig(t *testing.T, started time.Time, configurationOverrides map[string]string) CatalogCohortReceipt {
	t.Helper()
	receipt := validCatalogCohortFixture(started)
	receipt.OperationID = catalogCohortContinuationOperationID
	receipt.Lane = config.ReleaseLanePRD
	receipt.ReleaseID = catalogCohortContinuationReleaseID
	receipt.FenceProofs = []CatalogCohortFenceProof{{
		Scope: "mac-project-publishers", Control: "test-only publisher fence", Owner: "fixture owner",
		Paused: true, NoNewAccepted: true, EvidenceSHA256: strings.Repeat("a", 64),
		VerifiedAt: started.UTC().Format(time.RFC3339Nano),
	}}
	for i := range receipt.Hosts {
		host := &receipt.Hosts[i]
		host.Target.ReleaseID = receipt.ReleaseID
		if configurationSHA256 := configurationOverrides[host.HostID]; configurationSHA256 != "" {
			host.ConfigurationSHA256 = configurationSHA256
		}
		host.ServiceActiveAtPreflight = true
		host.PreviousTarget = ""
		host.PreviousBundleSHA256 = strings.Repeat("3", 64)
		host.PreviousReadSchemaMax = store.CurrentSchemaVersion
		host.LegacyRollback = &CatalogLegacyRollbackCompatibility{
			FormatVersion: 1, LegacyBundleSHA256: host.PreviousBundleSHA256,
			ConfigurationSHA256: host.ConfigurationSHA256, DatabaseSchema: host.SchemaVersion,
			ReadSchemaMin: 1, ReadSchemaMax: store.CurrentSchemaVersion,
			TestSuite: catalogLegacyRollbackTestSuite, TestEvidenceSHA256: strings.Repeat("8", 64),
			SemanticChecks: append([]string(nil), catalogLegacyRollbackSemanticChecks...), TestedAt: started.UTC().Format(time.RFC3339Nano),
		}
	}
	if err := ValidateCatalogCohortReceipt(receipt); err != nil {
		t.Fatalf("prepared PRD receipt fixture invalid: %v", err)
	}
	proof := strings.Repeat("9", 64)
	for i, hostID := range catalogCohortSwitchOrder {
		host := receipt.Hosts[i]
		observedAt := started.Add(time.Duration(i) * time.Second)
		fence := CatalogCohortHostFence{
			HostID: hostID, ServiceWasActive: true, ServiceStopped: true,
			WorkInventorySHA256: host.WorkInventorySHA256, ConfigurationSHA256: host.ConfigurationSHA256,
			SelectorTarget: host.PreviousTarget, PreviousBundleSHA256: host.PreviousBundleSHA256,
			DatabaseSchema: host.SchemaVersion, ObservedAt: observedAt.Format(time.RFC3339Nano),
		}
		var err error
		receipt, err = AdvanceCatalogCohortHostFence(receipt, fence, observedAt)
		if err != nil {
			t.Fatalf("add fixture fence for %s: %v", hostID, err)
		}
	}
	var err error
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "hold", "", proof, started.Add(5*time.Second))
	if err != nil {
		t.Fatalf("hold PRD fixture: %v", err)
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "preflight", "linux-host", proof, started.Add(6*time.Second))
	if err != nil {
		t.Fatalf("preflight Linux fixture: %v", err)
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "switch", "linux-host", proof, started.Add(7*time.Second))
	if err != nil {
		t.Fatalf("switch Linux fixture: %v", err)
	}
	return receipt
}

func continuationObservationFixtures(t *testing.T, receipt CatalogCohortReceipt, observedAt time.Time) []CatalogCohortContinuationHostObservation {
	t.Helper()
	observations := make([]CatalogCohortContinuationHostObservation, 0, len(catalogCohortSwitchOrder))
	for i, hostID := range catalogCohortSwitchOrder {
		host := receipt.Hosts[i]
		observation := CatalogCohortContinuationHostObservation{
			FormatVersion: catalogCohortContinuationFormatVersion, OperationID: receipt.OperationID, HostID: hostID,
			Lane: receipt.Lane, ReleaseID: receipt.ReleaseID, ReleaseSHA256: receipt.ReleaseSHA256,
			BaseTransitionCount: len(receipt.Transitions), BaseTransitionSHA256: lastCohortTransitionDigest(receipt.Transitions),
			SelectorKind: "unselected", SelectorTarget: host.PreviousTarget,
			SelectorBundleSHA256: host.PreviousBundleSHA256, ConfigurationSHA256: host.ConfigurationSHA256,
			DatabaseSchema: host.SchemaVersion, WorkInventorySHA256: host.WorkInventorySHA256, WorkInventoryEmpty: true,
			BackupDirectory: host.BackupDirectory, BackupManifestSHA256: host.BackupManifestSHA256,
			AvailableBytes: 2 << 30, RequiredBytes: prdCatalogOperatingBufferBytes + prdCatalogSelectorHeadroom,
			ServiceStopped: true, AdmissionHeld: true, AdmissionOperationID: receipt.OperationID,
			ObservedAt: observedAt.Format(time.RFC3339Nano),
		}
		if hostID == "linux-host" {
			plan, err := BuildCatalogPlan(hostID, config.ReleaseLanePRD)
			if err != nil {
				t.Fatal(err)
			}
			root, err := DefaultCatalogRootForPlan(plan)
			if err != nil {
				t.Fatal(err)
			}
			observation.SelectorKind = "catalog"
			observation.SelectorTarget = filepath.Join(root, receipt.ReleaseID, hostID)
			observation.SelectorBundleSHA256 = host.Target.BundleSHA256
			observation.SelectorJournalOperationID = strings.Repeat("a", 32)
			observation.SelectorJournalSHA256 = receipt.Transitions[len(receipt.Transitions)-1].EvidenceSHA256
		}
		observations = append(observations, observation)
	}
	return observations
}

func TestCatalogCohortContinuationEvidenceDigestCoversEveryObservation(t *testing.T) {
	started := time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC)
	receipt := expiredPRDSwitchingReceiptFixture(t, started)
	observations := continuationObservationFixtures(t, receipt, started.Add(61*time.Minute+5*time.Second))
	renewal, err := NewCatalogCohortContinuationRenewal(receipt, observations, started.Add(61*time.Minute+15*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	original := renewal.SHA256
	renewal.Hosts[3].ConfigurationSHA256 = strings.Repeat("f", 64)
	if catalogCohortContinuationDigest(renewal) == original || ValidateCatalogCohortContinuationRenewal(renewal) == nil {
		t.Fatal("renewal mutation was not detected by its content digest")
	}
}

func TestCatalogCohortContinuationObservationFileIsCanonicalOwnerOnlyAndImmutable(t *testing.T) {
	started := time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC)
	receipt := expiredPRDSwitchingReceiptFixture(t, started)
	observation := continuationObservationFixtures(t, receipt, started.Add(61*time.Minute+5*time.Second))[0]
	root := filepath.Join(catalogTestTempDir(t), "private")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "linux-host.json")
	if err := WriteCatalogCohortContinuationHostObservationFile(path, observation); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCatalogCohortContinuationHostObservationFile(path)
	if err != nil || loaded != observation {
		t.Fatalf("canonical observation load: host=%q err=%v", loaded.HostID, err)
	}
	changed := observation
	changed.ConfigurationSHA256 = strings.Repeat("f", 64)
	if err := WriteCatalogCohortContinuationHostObservationFile(path, changed); err == nil {
		t.Fatal("immutable report path accepted different observation bytes")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalogCohortContinuationHostObservationFile(path); err == nil {
		t.Fatal("observation with broad permissions was accepted")
	}
}

func TestCatalogCohortContinuationCapacityKeepsBufferAndSelectorReserveSeparate(t *testing.T) {
	required, err := catalogCohortContinuationRequiredBytes(prdCatalogSelectorHeadroom)
	if err != nil || required != 500_000_000+64*1024*1024 {
		t.Fatalf("continuation free-space requirement=%d err=%v", required, err)
	}
	if _, err := catalogCohortContinuationRequiredBytes(prdCatalogOperatingBufferBytes); err == nil {
		t.Fatal("operating buffer was accepted in place of the 64 MiB selector reserve")
	}
	observation := continuationObservationFixtures(t, expiredPRDSwitchingReceiptFixture(t,
		time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC)),
		time.Date(2026, 10, 23, 6, 1, 5, 0, time.UTC))[1]
	observation.AvailableBytes = observation.RequiredBytes - 1
	if validateCatalogCohortContinuationHostObservationFields(observation) == nil {
		t.Fatal("renewal accepted less than the operating buffer plus selector reserve")
	}
}

func TestContinuationCoordinatorRefreshesVolatileCapacityReading(t *testing.T) {
	started := time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC)
	receipt := expiredPRDSwitchingReceiptFixture(t, started)
	observedAt := started.Add(61*time.Minute + 5*time.Second)
	observations := continuationObservationFixtures(t, receipt, observedAt)
	mac := observations[len(observations)-1]
	mac.ObservedAt = observedAt.Add(30 * time.Second).Format(time.RFC3339Nano)
	mac.AvailableBytes += 4_411_392
	if !SameCatalogCohortContinuationHostState(observations[len(observations)-1], mac) {
		t.Fatal("coordinator rejected a fresh Mac observation only because its free-space counter changed")
	}
	mac.ConfigurationSHA256 = strings.Repeat("f", 64)
	if SameCatalogCohortContinuationHostState(observations[len(observations)-1], mac) {
		t.Fatal("coordinator ignored a stable configuration identity change")
	}

	mac = observations[len(observations)-1]
	mac.ObservedAt = observedAt.Add(30 * time.Second).Format(time.RFC3339Nano)
	mac.AvailableBytes += 4_411_392
	observations[len(observations)-1] = mac
	verifiedAt := observedAt.Add(31 * time.Second)
	renewal, err := NewCatalogCohortContinuationRenewal(receipt, observations, verifiedAt)
	if err != nil {
		t.Fatalf("renewal rejected a fresh, above-threshold capacity reading: %v", err)
	}
	if renewal.Hosts[len(renewal.Hosts)-1].AvailableBytes != mac.AvailableBytes ||
		renewal.Hosts[len(renewal.Hosts)-1].ObservedAt != mac.ObservedAt {
		t.Fatal("renewal did not bind the coordinator's fresh Mac observation")
	}

	mac.AvailableBytes = mac.RequiredBytes - 1
	if validateCatalogCohortContinuationHostObservationFields(mac) == nil {
		t.Fatal("fresh observation accepted a capacity reading below its required reserve")
	}
}

func TestContinuationObservationAcceptsSwitchedServiceArtifactsWhilePreservingBackup(t *testing.T) {
	started := time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC)
	receipt := expiredPRDSwitchingReceiptFixture(t, started)
	backupRoot := filepath.Join(t.TempDir(), "backup")
	backupArtifactDir := filepath.Join(backupRoot, "service")
	if err := os.MkdirAll(backupArtifactDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(backupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "runnerd.service")
	original := []byte("legacy service artifact\n")
	selected := []byte("catalog-selected service artifact\n")
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(backupArtifactDir, "runnerd.service")
	if err := os.WriteFile(backupPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(backupPath, 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := fileSHA256(target)
	if err != nil {
		t.Fatal(err)
	}
	file := PRDBackupFile{
		TargetPath: target, BackupPath: filepath.Join("service", "runnerd.service"),
		SHA256: digest, Bytes: int64(len(original)), Mode: 0o644,
	}
	manifest := PRDBackupManifest{Files: []PRDBackupFile{file}}
	if err := validatePRDBackupFile(backupRoot, backupPath, file); err != nil {
		t.Fatalf("original backup copy is not valid: %v", err)
	}
	if err := validateCatalogCohortContinuationBackupTargets(receipt, "linux-host", manifest); err != nil {
		t.Fatalf("unmodified legacy target was rejected before selector switch: %v", err)
	}

	if err := os.WriteFile(target, selected, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateCatalogCohortContinuationBackupTargets(receipt, "linux-host", manifest); err != nil {
		t.Fatalf("recorded Linux switch was rejected despite its verified backup copy: %v", err)
	}
	if err := validatePRDBackupFile(backupRoot, backupPath, file); err != nil {
		t.Fatalf("switch-side target change altered the retained backup copy: %v", err)
	}
	if err := validateCatalogCohortContinuationBackupTargets(receipt, "sandbox-host", manifest); err == nil {
		t.Fatal("unswitched Sandbox accepted a changed legacy service artifact")
	}
}
