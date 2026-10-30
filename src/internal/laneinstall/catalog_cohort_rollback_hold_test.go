package laneinstall

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
)

func TestCatalogPRDRolledBackReplacementDiscoveryKeepsOneClosedHold(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		name := "catalog rollback"
		if fresh {
			name = "unselected replacement rollback"
		}
		t.Run(name, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			predecessor, prepared := rollbackHoldReceiptFixture(t, now, fresh)
			successor := advanceRollbackHoldFixture(t, prepared, now)
			if catalogCohortRolledBackReplacementHoldPair(successor, predecessor, now) {
				t.Fatal("reversed predecessor/successor roles were accepted")
			}
			// Retrying the same sealed candidate after rollback is permitted;
			// identity comes from its exact restored baseline, not a new name.
			if predecessor.ReleaseID != successor.ReleaseID || predecessor.ReleaseSHA256 != successor.ReleaseSHA256 {
				t.Fatal("fixture did not retry the same sealed candidate")
			}
			root := t.TempDir()
			writeCohortReceiptFixture(t, root, predecessor)
			writeCohortReceiptFixture(t, root, successor)
			assertRollbackHoldDiscovery(t, root, successor.OperationID, true)
			history := cloneRollbackHoldReceipt(t, predecessor).Transitions
			for index := range catalogCohortSwitchOrder {
				disposition := "none"
				if cohortHasSwitched(predecessor, catalogCohortSwitchOrder[index]) {
					disposition = "rolled_back"
				}
				var err error
				predecessor, err = AdvanceCatalogCohortSupersedeHost(predecessor, successor,
					fixtureCohortSupersessionEvidence(predecessor, successor, index, disposition), now)
				if err != nil {
					t.Fatalf("record exact rollback supersession on %s: %v", catalogCohortSwitchOrder[index], err)
				}
				writeCohortReceiptFixture(t, root, predecessor)
				assertRollbackHoldDiscovery(t, root, successor.OperationID, true)
			}
			var err error
			predecessor, err = AdvanceCatalogCohortSupersede(predecessor, successor.OperationID, now)
			if err != nil || !reflect.DeepEqual(history, predecessor.Transitions[:len(history)]) {
				t.Fatalf("retire predecessor without altering its rollback history: %v", err)
			}
			writeCohortReceiptFixture(t, root, predecessor)
			assertRollbackHoldDiscovery(t, root, successor.OperationID, true)
			if successor.Stage != CatalogCohortHeld {
				t.Fatal("supersession opened successor admissions")
			}
		})
	}
}

func TestCatalogPRDRolledBackReplacementDiscoveryRejectsUnsafePairs(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	predecessor, prepared := rollbackHoldReceiptFixture(t, now, false)
	for _, tc := range []struct {
		name string
		edit func(*CatalogCohortReceipt)
	}{
		{"wrong selector", func(r *CatalogCohortReceipt) {
			r.Hosts[0].PreviousTarget = filepath.Join(filepath.Dir(filepath.Dir(r.Hosts[0].PreviousTarget)), "another-baseline", "linux-host")
		}},
		{"wrong bundle", func(r *CatalogCohortReceipt) { r.Hosts[0].PreviousBundleSHA256 = strings.Repeat("f", 64) }},
		{"configuration drift", func(r *CatalogCohortReceipt) { r.Hosts[1].ConfigurationSHA256 = strings.Repeat("a", 64) }},
		{"schema drift", func(r *CatalogCohortReceipt) { r.Hosts[2].SchemaVersion-- }},
		{"work identity drift", func(r *CatalogCohortReceipt) { r.Hosts[3].WorkInventorySHA256 = strings.Repeat("b", 64) }},
		{"service active", func(r *CatalogCohortReceipt) { r.Hosts[0].ServiceActiveAtPreflight = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := cloneRollbackHoldReceipt(t, prepared)
			tc.edit(&changed)
			successor := advanceRollbackHoldFixture(t, changed, now)
			root := t.TempDir()
			writeCohortReceiptFixture(t, root, predecessor)
			writeCohortReceiptFixture(t, root, successor)
			assertRollbackHoldDiscovery(t, root, successor.OperationID, false)
		})
	}
	root := t.TempDir()
	successor := advanceRollbackHoldFixture(t, prepared, now)
	// A different accepted candidate may replace the same restored baseline.
	differentCandidate := cloneRollbackHoldReceipt(t, prepared)
	differentCandidate.ReleaseID = "cat-rollback-hold-successor"
	differentCandidate.ReleaseSHA256 = strings.Repeat("a", 64)
	for index := range differentCandidate.Hosts {
		differentCandidate.Hosts[index].Target.ReleaseID = differentCandidate.ReleaseID
		differentCandidate.Hosts[index].Target.ReleaseSHA256 = differentCandidate.ReleaseSHA256
	}
	different := advanceRollbackHoldFixture(t, differentCandidate, now)
	writeCohortReceiptFixture(t, root, predecessor)
	writeCohortReceiptFixture(t, root, different)
	assertRollbackHoldDiscovery(t, root, different.OperationID, true)
	writeCohortReceiptFixture(t, root, predecessor)
	writeCohortReceiptFixture(t, root, successor)
	other := cloneRollbackHoldReceipt(t, successor)
	other.OperationID = strings.Repeat("c", 32)
	writeCohortReceiptFixture(t, root, other)
	assertRollbackHoldDiscovery(t, root, successor.OperationID, false)
	if !catalogCohortRolledBackReplacementHoldPair(predecessor, successor, now.Add(catalogCohortEvidenceMaxAge+time.Second)) {
		t.Fatal("expired evidence made the exact closed hold unrecognizable")
	}
	staleTime := now.Add(-catalogCohortEvidenceMaxAge - time.Second)
	stalePredecessor, stalePrepared := rollbackHoldReceiptFixture(t, staleTime, false)
	staleSuccessor := advanceRollbackHoldFixture(t, stalePrepared, staleTime)
	staleRoot := t.TempDir()
	writeCohortReceiptFixture(t, staleRoot, stalePredecessor)
	writeCohortReceiptFixture(t, staleRoot, staleSuccessor)
	assertRollbackHoldDiscovery(t, staleRoot, staleSuccessor.OperationID, true)
	observation := fixtureCohortSupersessionEvidence(stalePredecessor, staleSuccessor, 0, "rolled_back")
	if _, err := AdvanceCatalogCohortSupersedeHost(stalePredecessor, staleSuccessor, observation, now); err == nil {
		t.Fatal("closed-hold discovery authorized supersession using expired evidence")
	}
	partial := cloneRollbackHoldReceipt(t, predecessor)
	partial.Transitions = partial.Transitions[:len(partial.Transitions)-1]
	partial.Stage = CatalogCohortRollingBack
	partial.UpdatedAt = partial.Transitions[len(partial.Transitions)-1].At
	if ValidateCatalogCohortReceipt(partial) != nil || catalogCohortRolledBackReplacementHoldPair(partial, successor, now) {
		t.Fatal("incomplete rollback accepted or fixture invalid")
	}
	partialRoot := t.TempDir()
	writeCohortReceiptFixture(t, partialRoot, partial)
	writeCohortReceiptFixture(t, partialRoot, successor)
	assertRollbackHoldDiscovery(t, partialRoot, successor.OperationID, false)
	freshPredecessor, freshPrepared := rollbackHoldReceiptFixture(t, now, true)
	freshPrepared.Hosts[0].FreshInstall = nil
	freshPrepared.Hosts[0].PreviousTarget = prepared.Hosts[0].PreviousTarget
	freshPrepared.Hosts[0].PreviousBundleSHA256 = prepared.Hosts[0].PreviousBundleSHA256
	freshPrepared.Hosts[0].PreviousReadSchemaMax = prepared.Hosts[0].PreviousReadSchemaMax
	changedFresh := advanceRollbackHoldFixture(t, freshPrepared, now)
	if catalogCohortRolledBackReplacementHoldPair(freshPredecessor, changedFresh, now) {
		t.Fatal("unselected replacement rollback accepted a substituted catalog baseline")
	}
	freshRoot := t.TempDir()
	writeCohortReceiptFixture(t, freshRoot, freshPredecessor)
	writeCohortReceiptFixture(t, freshRoot, changedFresh)
	assertRollbackHoldDiscovery(t, freshRoot, changedFresh.OperationID, false)
}

func rollbackHoldReceiptFixture(t *testing.T, now time.Time, fresh bool) (CatalogCohortReceipt, CatalogCohortReceipt) {
	t.Helper()
	prepared := catalogPRDSupersessionReceiptFixture(t, "cat-rollback-hold-fixture", nil, now)
	if fresh {
		host := &prepared.Hosts[0]
		absence := validFreshPRDAbsenceForTest(t, now.Add(-30*time.Second))
		baseline, err := NewCatalogFreshInstallBaseline(strings.Repeat("a", 32), absence,
			host.ConfigurationSHA256, host.SchemaVersion, CatalogWorkInventory{}, now.Add(-15*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		host.PreviousTarget, host.PreviousBundleSHA256, host.PreviousReadSchemaMax = "", "", 0
		host.FreshInstall = &baseline
	}
	predecessor := advanceRollbackHoldFixture(t, cloneRollbackHoldReceipt(t, prepared), now)
	proof := strings.Repeat("d", 64)
	var err error
	for _, host := range catalogCohortSwitchOrder[:2] {
		for _, action := range []string{"preflight", "switch"} {
			predecessor, err = AdvanceCatalogCohortReceipt(predecessor, action, host, proof, now)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, action := range []string{"block", "rollback-start"} {
		predecessor, err = AdvanceCatalogCohortReceipt(predecessor, action, "", proof, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, host := range []string{"sandbox-host", "linux-host"} {
		predecessor, err = AdvanceCatalogCohortReceipt(predecessor, "rollback-host", host, proof, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	predecessor, err = AdvanceCatalogCohortReceipt(predecessor, "rollback-complete", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	prepared.OperationID = strings.Repeat("b", 32)
	return predecessor, prepared
}

func advanceRollbackHoldFixture(t *testing.T, receipt CatalogCohortReceipt, now time.Time) CatalogCohortReceipt {
	t.Helper()
	for _, host := range receipt.Hosts {
		freshSHA := ""
		if host.FreshInstall != nil {
			freshSHA = host.FreshInstall.EvidenceSHA256
		}
		fence := CatalogCohortHostFence{HostID: host.HostID, ServiceWasActive: host.ServiceActiveAtPreflight,
			ServiceStopped: true, WorkInventorySHA256: host.WorkInventorySHA256, ConfigurationSHA256: host.ConfigurationSHA256,
			SelectorTarget: host.PreviousTarget, PreviousBundleSHA256: host.PreviousBundleSHA256,
			FreshInstallEvidenceSHA256: freshSHA, DatabaseSchema: host.SchemaVersion, ObservedAt: now.Format(time.RFC3339Nano)}
		var err error
		receipt, err = AdvanceCatalogCohortHostFence(receipt, fence, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	receipt, err := AdvanceCatalogCohortReceipt(receipt, "hold", "", strings.Repeat("e", 64), now)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func cloneRollbackHoldReceipt(t *testing.T, receipt CatalogCohortReceipt) CatalogCohortReceipt {
	t.Helper()
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var result CatalogCohortReceipt
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertRollbackHoldDiscovery(t *testing.T, root, want string, valid bool) {
	t.Helper()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, host := range catalogCohortSwitchOrder {
		plan, err := BuildCatalogPlan(host, config.ReleaseLanePRD)
		if err != nil {
			t.Fatal(err)
		}
		held, id, err := catalogCohortAdmissionHeldAt(plan, root)
		if valid && (err != nil || !held || id != want) || !valid && err == nil {
			t.Fatalf("%s admission discovery: held=%v id=%q err=%v valid=%v", host, held, id, err, valid)
		}
	}
}
