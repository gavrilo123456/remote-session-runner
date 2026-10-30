package laneinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
)

func TestCatalogClosedSupersessionHoldSurvivesEvidenceExpiry(t *testing.T) {
	for _, kind := range []string{"dev-complete", "qas-complete", "prd-verifying", "prd-rolled-back", "prd-fresh-rollback"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now().UTC().Truncate(time.Second)
			created := now.Add(-catalogCohortEvidenceMaxAge - time.Minute)
			var predecessor, successor CatalogCohortReceipt
			disposition := "completed"
			switch kind {
			case "prd-rolled-back", "prd-fresh-rollback":
				var prepared CatalogCohortReceipt
				predecessor, prepared = rollbackHoldReceiptFixture(t, created, kind == "prd-fresh-rollback")
				successor = advanceRollbackHoldFixture(t, prepared, created)
				disposition = "rolled_back"
			case "prd-verifying":
				predecessor = catalogPRDAdvanceToVerifying(t,
					catalogPRDSupersessionReceiptFixture(t, "cat-expiry-predecessor", nil, created), created)
				successor = catalogPRDAdvanceToHeld(t,
					catalogPRDSupersessionReceiptFixture(t, "cat-expiry-successor", &predecessor, created), created)
			default:
				lane := config.ReleaseLaneDev
				if kind == "qas-complete" {
					lane = config.ReleaseLaneQAS
				}
				predecessor, successor = expiredCompletedHoldPairFixture(t, lane, created)
			}
			if cohortEvidenceFresh(successor, now.Format(time.RFC3339Nano)) {
				t.Fatal("fixture does not have expired evidence")
			}
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			writeCohortReceiptFixture(t, root, predecessor)
			writeCohortReceiptFixture(t, root, successor)
			for _, host := range catalogCohortSwitchOrder {
				plan, err := BuildCatalogPlan(host, successor.Lane)
				if err != nil {
					t.Fatal(err)
				}
				held, id, err := catalogCohortAdmissionHeldAt(plan, root)
				if err != nil || !held || id != successor.OperationID {
					t.Fatalf("%s lost its closed hold after expiry: held=%v id=%q err=%v", host, held, id, err)
				}
				if err := catalogCohortAdmissionHoldMatchesAt(plan, root, successor.OperationID); err != nil {
					t.Fatalf("%s cannot identify the hold needed for supported renewal: %v", host, err)
				}
			}
			observation := fixtureCohortSupersessionEvidence(predecessor, successor, 0, disposition)
			if _, err := AdvanceCatalogCohortSupersedeHost(predecessor, successor, observation, now); err == nil {
				t.Fatal("recognizing a closed hold authorized a mutation without fresh evidence")
			}
		})
	}
}

func expiredCompletedHoldPairFixture(t *testing.T, lane config.ReleaseLane, created time.Time) (CatalogCohortReceipt, CatalogCohortReceipt) {
	t.Helper()
	predecessor := heldCatalogCohortFixture(t, created, strings.Repeat("1", 32), "v2.0.0")
	predecessor.Lane = lane
	proof := strings.Repeat("d", 64)
	advance := func(action, host string) {
		t.Helper()
		var err error
		predecessor, err = AdvanceCatalogCohortReceipt(predecessor, action, host, proof, created)
		if err != nil {
			t.Fatalf("%s predecessor %s: %v", lane, action, err)
		}
	}
	for _, host := range catalogCohortSwitchOrder {
		advance("preflight", host)
		advance("switch", host)
	}
	advance("verify-start", "")
	for _, host := range catalogCohortSwitchOrder {
		advance("verify", host)
	}
	advance("complete", "")
	successor := validCatalogCohortFixture(created)
	successor.Lane, successor.OperationID = lane, strings.Repeat("b", 32)
	successor.ReleaseID, successor.ReleaseSHA256 = "v2.0.1", strings.Repeat("b", 64)
	for index, host := range catalogCohortSwitchOrder {
		plan, err := BuildCatalogPlan(host, lane)
		if err != nil {
			t.Fatal(err)
		}
		root, err := DefaultCatalogRootForPlan(plan)
		if err != nil {
			t.Fatal(err)
		}
		next := &successor.Hosts[index]
		next.Target.ReleaseID, next.Target.ReleaseSHA256 = successor.ReleaseID, successor.ReleaseSHA256
		next.PreviousTarget = filepath.Join(root, predecessor.ReleaseID, host)
		next.PreviousBundleSHA256 = predecessor.Hosts[index].Target.BundleSHA256
		next.ConfigurationSHA256, next.SchemaVersion = predecessor.Hosts[index].ConfigurationSHA256, predecessor.Hosts[index].SchemaVersion
		next.WorkInventorySHA256, next.WorkInventoryEmpty = predecessor.Hosts[index].WorkInventorySHA256, true
	}
	var err error
	successor, err = AdvanceCatalogCohortReceipt(successor, "hold", "", proof, created)
	if err != nil {
		t.Fatal(err)
	}
	return predecessor, successor
}
