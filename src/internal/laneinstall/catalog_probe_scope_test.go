package laneinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
)

type catalogProbeFixture struct {
	guard      *CatalogMaintenanceProbeGuard
	operation  string
	controller string
	key        string
	token      string
	body       []byte
	now        time.Time
}

func newCatalogProbeFixture(t *testing.T) catalogProbeFixture {
	t.Helper()
	selector := newCatalogSelectorFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	receipt, target := validCatalogCohortForSelectorFixture(t, selector, now)
	proof := strings.Repeat("f", 64)
	var err error
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "hold", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range catalogCohortSwitchOrder {
		receipt, err = AdvanceCatalogCohortReceipt(receipt, "preflight", host, proof, now)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err = AdvanceCatalogCohortReceipt(receipt, "switch", host, proof, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "verify-start", "", proof, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range catalogCohortSwitchOrder {
		receipt, err = AdvanceCatalogCohortReceipt(receipt, "verify", host, proof, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	if receipt.Stage != CatalogCohortVerifying {
		t.Fatalf("fixture cohort stage=%q, want verifying", receipt.Stage)
	}
	if err := switchCatalogSelector(selector.layout, selector.legacyTarget, filepath.Join(selector.catalogRoot, target.ReleaseID, selector.layout.hostID)); err != nil {
		t.Fatal(err)
	}
	cohortRoot := filepath.Join(t.TempDir(), "catalog-cohorts")
	scopeRoot := filepath.Join(t.TempDir(), "catalog-probes")
	for _, root := range []string{cohortRoot, scopeRoot} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cohortData, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	cohortData = append(cohortData, '\n')
	if err := os.WriteFile(filepath.Join(cohortRoot, receipt.OperationID+".json"), cohortData, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildCatalogPlan("linux-host", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := NewCatalogMaintenanceProbeGuard(plan, receipt.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	guard.testPaths = &catalogProbeTestPaths{cohortRoot: cohortRoot, scopeRoot: scopeRoot, layout: selector.layout}
	controller := "maintenance-operator"
	key := "catalog-probe-" + receipt.OperationID + "-linux-host"
	token := strings.Repeat("A", 43)
	body, err := CatalogMaintenanceProbeRequestBody(plan)
	if err != nil {
		t.Fatal(err)
	}
	tokenDigest := sha256.Sum256([]byte(token))
	bodyDigest := sha256.Sum256(body)
	expires := now.Add(time.Minute)
	scope := catalogProbeScope{
		FormatVersion: catalogProbeScopeFormat, OperationID: receipt.OperationID, HostID: plan.HostID, Lane: plan.Lane,
		ControllerID: controller, CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: expires.Format(time.RFC3339Nano),
		TokenSHA256: hex.EncodeToString(tokenDigest[:]), IdempotencyKey: key,
		RequestBodySHA256: hex.EncodeToString(bodyDigest[:]), State: "ready",
	}
	if err := writeCatalogProbeScope(filepath.Join(scopeRoot, receipt.OperationID+".json"), scopeRoot, scope); err != nil {
		t.Fatal(err)
	}
	return catalogProbeFixture{guard: guard, operation: receipt.OperationID, controller: controller, key: key, token: token, body: body, now: now}
}

func TestCatalogMaintenanceProbeIsOneShotAndJobReadIsIdentityBound(t *testing.T) {
	fixture := newCatalogProbeFixture(t)
	if err := fixture.guard.ReserveCatalogMaintenanceProbe(fixture.controller, fixture.token, fixture.key, fixture.body, fixture.now); err != nil {
		t.Fatalf("reserve exact probe: %v", err)
	}
	if err := fixture.guard.ReserveCatalogMaintenanceProbe(fixture.controller, fixture.token, fixture.key, fixture.body, fixture.now); err == nil {
		t.Fatal("replayed probe reservation was accepted")
	}
	jobID := "job-0123456789abcdef0123456789abcdef"
	if err := fixture.guard.BindCatalogMaintenanceProbeJob(jobID); err != nil {
		t.Fatalf("bind accepted job identity: %v", err)
	}
	if !fixture.guard.AllowsJobRead(fixture.controller, jobID, fixture.now) {
		t.Fatal("exact reserved probe job was not readable")
	}
	if fixture.guard.AllowsJobRead("another-controller", jobID, fixture.now) ||
		fixture.guard.AllowsJobRead(fixture.controller, "job-ffffffffffffffffffffffffffffffff", fixture.now) {
		t.Fatal("scope allowed an unrelated controller or job read")
	}
	if err := fixture.guard.MarkCatalogMaintenanceProbeAccepted(jobID); err != nil {
		t.Fatalf("mark durable acceptance: %v", err)
	}
	if !fixture.guard.AllowsJobRead(fixture.controller, jobID, fixture.now) {
		t.Fatal("accepted probe job became unreadable")
	}
	if fixture.guard.AllowsJobRead(fixture.controller, jobID, fixture.now.Add(time.Minute)) {
		t.Fatal("expired probe scope allowed a job read")
	}
	if err := fixture.guard.MarkCatalogMaintenanceProbeAccepted(jobID); err == nil {
		t.Fatal("acceptance state transition was not one-shot")
	}
}

func TestCatalogMaintenanceProbeRejectsMismatchedEvidenceWithoutConsumingScope(t *testing.T) {
	for _, mismatch := range []string{"controller", "token", "idempotency", "body"} {
		t.Run(mismatch, func(t *testing.T) {
			fixture := newCatalogProbeFixture(t)
			controller, token, key, body := fixture.controller, fixture.token, fixture.key, fixture.body
			switch mismatch {
			case "controller":
				controller = "other-controller"
			case "token":
				token = strings.Repeat("B", 43)
			case "idempotency":
				key += "-changed"
			case "body":
				body = append(append([]byte(nil), body...), ' ')
			}
			if err := fixture.guard.ReserveCatalogMaintenanceProbe(controller, token, key, body, fixture.now); err == nil {
				t.Fatal("mismatched evidence was accepted")
			}
			if err := fixture.guard.ReserveCatalogMaintenanceProbe(fixture.controller, fixture.token, fixture.key, fixture.body, fixture.now); err != nil {
				t.Fatalf("refused mismatch consumed the valid scope: %v", err)
			}
		})
	}
}

func TestCatalogMaintenanceProbeFailsClosedAfterExpiryOrCohortChange(t *testing.T) {
	fixture := newCatalogProbeFixture(t)
	if err := fixture.guard.ReserveCatalogMaintenanceProbe(fixture.controller, fixture.token, fixture.key, fixture.body, fixture.now.Add(time.Minute)); err == nil {
		t.Fatal("expired probe reservation was accepted")
	}
	path := filepath.Join(fixture.guard.testPaths.cohortRoot, fixture.operation+".json")
	receipt, err := readCatalogCohortReceipt(path)
	if err != nil {
		t.Fatal(err)
	}
	proof := strings.Repeat("d", 64)
	receipt, err = AdvanceCatalogCohortReceipt(receipt, "complete", "", proof, fixture.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.guard.ReserveCatalogMaintenanceProbe(fixture.controller, fixture.token, fixture.key, fixture.body, fixture.now.Add(time.Minute)); err == nil {
		t.Fatal("cohort completion did not close the probe admission window")
	}
}
