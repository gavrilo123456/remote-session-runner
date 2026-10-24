package releaseflow

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/laneinstall"
	"remote-session-runner/src/internal/store"
)

type cohortCommandFixture struct {
	receipts          map[string]map[string]laneinstall.CatalogCohortReceipt
	commands          []string
	interruptHostOnce string
}

func (f *cohortCommandFixture) run(_ context.Context, host string, args []string, stdin []byte) ([]byte, error) {
	if len(args) < 1 || len(args) < 2 {
		return nil, ErrInvalidInput
	}
	if args[0] != "catalog-cohort" {
		return nil, ErrInvalidInput
	}
	value := func(flag string) string {
		for index := 0; index+1 < len(args); index++ {
			if args[index] == flag {
				return args[index+1]
			}
		}
		return ""
	}
	lane, action := value("--lane"), value("--action")
	id := value("--operation-id")
	f.commands = append(f.commands, host+":"+action+":"+id+":"+value("--subject-host"))
	if lane != "dev" {
		return nil, ErrInvalidInput
	}
	switch action {
	case "inspect":
		receipt, ok := f.receipts[host][id]
		data, err := json.Marshal(promotionReceiptView{Present: ok, Receipt: receipt})
		return data, err
	case "discover":
		var held string
		var heldReceipt *laneinstall.CatalogCohortReceipt
		for operationID, receipt := range f.receipts[host] {
			if receipt.Stage == laneinstall.CatalogCohortHeld {
				held = operationID
				receiptCopy := receipt
				heldReceipt = &receiptCopy
			}
		}
		data, err := json.Marshal(promotionAdmissionView{HostID: host, Lane: "dev", AdmissionHeld: held != "", OperationID: held, Receipt: heldReceipt})
		return data, err
	case "sync":
		var receipt laneinstall.CatalogCohortReceipt
		if value("--receipt-file") != "@promo-stdin@" || json.Unmarshal(stdin, &receipt) != nil || receipt.OperationID == "" {
			return nil, ErrInvalidInput
		}
		f.receipts[host][receipt.OperationID] = receipt
		return json.Marshal(map[string]any{"operation_id": receipt.OperationID, "stage": receipt.Stage})
	case "supersede-host":
		successorID := value("--successor-operation-id")
		predecessor, predecessorOK := f.receipts[host][id]
		successor, successorOK := f.receipts[host][successorID]
		if !predecessorOK || !successorOK || value("--subject-host") != host {
			return nil, ErrOperationCorrupted
		}
		index := 0
		for hostIndex, candidate := range HostOrder {
			if candidate == host {
				index = hostIndex
				break
			}
		}
		hostReceipt := successor.Hosts[index]
		observation := laneinstall.CatalogCohortSupersessionEvidence{
			HostID: host, SuccessorOperationID: successorID,
			ExpectedSelector: hostReceipt.PreviousTarget, ObservedSelector: hostReceipt.PreviousTarget,
			SelectorBundleSHA256: predecessor.Hosts[index].Target.BundleSHA256, SelectorOperations: "completed",
			ServiceActive: false, WorkInventorySHA256: hostReceipt.WorkInventorySHA256, WorkInventoryEmpty: true,
			ConfigurationSHA256: hostReceipt.ConfigurationSHA256, DatabaseSchema: hostReceipt.SchemaVersion,
		}
		updated, err := laneinstall.AdvanceCatalogCohortSupersedeHost(predecessor, successor, observation, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		f.receipts[host][id] = updated
		if f.interruptHostOnce == host {
			f.interruptHostOnce = ""
			return nil, errors.New("injected interruption after local supersede-host write and before synchronization")
		}
		return json.Marshal(map[string]any{"operation_id": id, "stage": updated.Stage})
	case "supersede":
		successorID := value("--successor-operation-id")
		predecessor, ok := f.receipts[host][id]
		if !ok || value("--subject-host") != "" {
			return nil, ErrOperationCorrupted
		}
		updated, err := laneinstall.AdvanceCatalogCohortSupersede(predecessor, successorID, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		f.receipts[host][id] = updated
		return json.Marshal(map[string]any{"operation_id": id, "stage": updated.Stage})
	default:
		return nil, ErrInvalidInput
	}
}

func TestNativePromotionSupersessionResumesAfterLocalReceiptWriteBeforeSync(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	predecessor := promotionTestCohortReceipt(t, now, strings.Repeat("1", 32), "v2.0.0", nil, true)
	proof := strings.Repeat("d", 64)
	var err error
	for _, host := range HostOrder {
		predecessor, err = laneinstall.AdvanceCatalogCohortReceipt(predecessor, "preflight", host, proof, now)
		if err != nil {
			t.Fatal(err)
		}
		predecessor, err = laneinstall.AdvanceCatalogCohortReceipt(predecessor, "switch", host, proof, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	predecessor, err = laneinstall.AdvanceCatalogCohortReceipt(predecessor, "verify-start", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range HostOrder {
		predecessor, err = laneinstall.AdvanceCatalogCohortReceipt(predecessor, "verify", host, proof, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	predecessor, err = laneinstall.AdvanceCatalogCohortReceipt(predecessor, "complete", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	successor := promotionTestCohortReceipt(t, now, strings.Repeat("b", 32), "v2.0.1", &predecessor, true)

	fixture := &cohortCommandFixture{receipts: make(map[string]map[string]laneinstall.CatalogCohortReceipt), interruptHostOnce: "linux-host"}
	for _, host := range HostOrder {
		fixture.receipts[host] = map[string]laneinstall.CatalogCohortReceipt{
			predecessor.OperationID: predecessor,
			successor.OperationID:   successor,
		}
	}
	backend := &NativePromotionBackend{laneCommandRunner: fixture.run}
	plan := PromotionPlan{Lane: "dev", ExistingHolds: map[string]string{}}
	for index, host := range HostOrder {
		plan.ExistingHolds[host] = predecessor.OperationID
		plan.HostBaselines = append(plan.HostBaselines, HostObservation{HostID: host, Lane: "dev",
			SelectedReleaseID: predecessor.ReleaseID, SelectedBundleSHA: predecessor.Hosts[index].Target.BundleSHA256,
			WorkInventorySHA: predecessor.Hosts[index].WorkInventorySHA256, WorkInventoryEmpty: true,
			ConfigurationSHA: predecessor.Hosts[index].ConfigurationSHA256, SchemaVersion: predecessor.Hosts[index].SchemaVersion})
	}
	if err := backend.ValidateExistingCohortHold(context.Background(), plan); err != nil {
		t.Fatalf("matching completed hold should pass the native read-only validation: %v", err)
	}
	changedPlan := plan
	changedPlan.HostBaselines = append([]HostObservation(nil), plan.HostBaselines...)
	changedPlan.HostBaselines[1].SelectedBundleSHA = strings.Repeat("f", 64)
	if err := backend.ValidateExistingCohortHold(context.Background(), changedPlan); err == nil {
		t.Fatal("native predecessor validation accepted a changed selected bundle")
	}
	op := Operation{Lane: "dev", CohortOperationID: successor.OperationID, PreviousCohortOperationID: predecessor.OperationID}
	if err := backend.supersedeCompletedPredecessor(context.Background(), op, successor); err == nil ||
		!strings.Contains(err.Error(), "injected interruption") {
		t.Fatalf("expected the injected after-write interruption, got %v", err)
	}
	if fixture.receipts["linux-host"][predecessor.OperationID].Stage != laneinstall.CatalogCohortComplete ||
		fixture.receipts["mac-control-plane"][predecessor.OperationID].Stage != laneinstall.CatalogCohortComplete {
		t.Fatal("interrupted predecessor history was not preserved as a completed held receipt")
	}
	if err := backend.supersedeCompletedPredecessor(context.Background(), op, successor); err != nil {
		t.Fatalf("resume after receipt write interruption: %v", err)
	}
	for _, host := range HostOrder {
		if fixture.receipts[host][predecessor.OperationID].Stage != laneinstall.CatalogCohortSuperseded {
			t.Fatalf("predecessor not superseded on %s", host)
		}
		if fixture.receipts[host][successor.OperationID].Stage != laneinstall.CatalogCohortHeld {
			t.Fatalf("successor hold not preserved on %s", host)
		}
	}
	for _, host := range HostOrder {
		count := 0
		for _, command := range fixture.commands {
			if strings.HasPrefix(command, host+":supersede-host:") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("host %s supersede-host ran %d times across retry, want exactly once", host, count)
		}
	}
}

func promotionTestCohortReceipt(t *testing.T, now time.Time, operationID, releaseID string,
	predecessor *laneinstall.CatalogCohortReceipt, held bool) laneinstall.CatalogCohortReceipt {
	t.Helper()
	const migrationPolicy = "forward-on-start-supported-migration-chain"
	const rollbackPolicy = "binary-only-if-current-data-fits-previous-release"
	stamp := now.UTC().Format(time.RFC3339Nano)
	releaseSHA := strings.Repeat("a", 64)
	if releaseID == "v2.0.1" {
		releaseSHA = strings.Repeat("b", 64)
	}
	receipt := laneinstall.CatalogCohortReceipt{
		FormatVersion: 1, OperationID: operationID, Lane: config.ReleaseLaneDev, ReleaseID: releaseID,
		ReleaseSHA256: releaseSHA, SourceRevision: strings.Repeat("2", 40), CreatedAt: stamp, UpdatedAt: stamp,
		Stage: laneinstall.CatalogCohortPrepared,
		Database: laneinstall.CatalogDatabaseCompatibility{ReadSchemaMin: 1, ReadSchemaMax: store.CurrentSchemaVersion,
			WriteSchemaMin: store.CurrentSchemaVersion, WriteSchemaMax: store.CurrentSchemaVersion,
			Migration: migrationPolicy, Rollback: rollbackPolicy},
	}
	for index, host := range HostOrder {
		previous := strings.Repeat("3", 64)
		previousTarget := "releases/" + previous
		if predecessor != nil {
			plan, err := laneinstall.BuildCatalogPlan(host, config.ReleaseLaneDev)
			if err != nil {
				t.Fatal(err)
			}
			root, err := laneinstall.DefaultCatalogRootForPlan(plan)
			if err != nil {
				t.Fatal(err)
			}
			previous = predecessor.Hosts[index].Target.BundleSHA256
			previousTarget = filepath.Join(root, predecessor.ReleaseID, host)
		}
		receipt.Hosts = append(receipt.Hosts, laneinstall.CatalogCohortHostPreflight{
			HostID: host, PreviousTarget: previousTarget, PreviousBundleSHA256: previous,
			Target: laneinstall.CatalogReleaseIdentity{ReleaseID: releaseID, ReleaseSHA256: releaseSHA,
				HostID: host, BundleSHA256: strings.Repeat(string("456789abcdef"[index]), 64)},
			ConfigurationSHA256: strings.Repeat("4", 64), SchemaVersion: store.CurrentSchemaVersion,
			WorkInventorySHA256: strings.Repeat("5", 64), WorkInventoryEmpty: true, PreflightAt: stamp,
			BackupDirectory: "/private/tmp/promotion-cohort/" + host, BackupManifestSHA256: strings.Repeat("6", 64),
			AvailableBytes: 2 << 30, RequiredBytes: 500_000_000,
		})
	}
	for _, scope := range laneinstall.CatalogCohortRequiredFences() {
		receipt.FenceProofs = append(receipt.FenceProofs, laneinstall.CatalogCohortFenceProof{
			Scope: scope, Control: "fixture-publisher-pause", Owner: "fixture-owner", Paused: true,
			NoNewAccepted: true, EvidenceSHA256: strings.Repeat("7", 64), VerifiedAt: stamp,
		})
	}
	if predecessor != nil {
		for index := range receipt.Hosts {
			receipt.Hosts[index].ConfigurationSHA256 = predecessor.Hosts[index].ConfigurationSHA256
			receipt.Hosts[index].SchemaVersion = predecessor.Hosts[index].SchemaVersion
			receipt.Hosts[index].WorkInventorySHA256 = predecessor.Hosts[index].WorkInventorySHA256
		}
	}
	if held {
		var err error
		receipt, err = laneinstall.AdvanceCatalogCohortReceipt(receipt, "hold", "", strings.Repeat("d", 64), now)
		if err != nil {
			t.Fatalf("build held successor fixture: %v", err)
		}
	}
	return receipt
}
