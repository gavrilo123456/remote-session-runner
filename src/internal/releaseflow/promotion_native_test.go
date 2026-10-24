package releaseflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/laneinstall"
	"remote-session-runner/src/internal/store"
)

func TestInitialPRDPlanRequiresExactLegacyCompatibilityInput(t *testing.T) {
	if _, err := legacyCompatibilityInputSHA(HostProfile{}); err == nil {
		t.Fatal("initial legacy PRD adoption accepted a missing rollback compatibility proof")
	}
	if !requiresLegacyRollbackEvidence(Operation{Lane: "prd", PlanSnapshot: &PromotionPlan{HostBaselines: []HostObservation{{HostID: "linux-host"}}}}, "linux-host") {
		t.Fatal("initial legacy PRD preflight did not require its semantic proof")
	}
	if requiresLegacyRollbackEvidence(Operation{Lane: "prd", PlanSnapshot: &PromotionPlan{HostBaselines: []HostObservation{{HostID: "linux-host", PreviousReleaseID: "cat-existing"}}}}, "linux-host") {
		t.Fatal("catalog-managed predecessor incorrectly required legacy adoption evidence")
	}

	path := filepath.Join(t.TempDir(), "legacy-compatibility.json")
	evidence := laneinstall.CatalogLegacyRollbackCompatibility{
		FormatVersion: 1, LegacyBundleSHA256: stringsOf("a"), ConfigurationSHA256: stringsOf("b"),
		DatabaseSchema: store.CurrentSchemaVersion, ReadSchemaMin: store.CurrentSchemaVersion, ReadSchemaMax: store.CurrentSchemaVersion,
		TestSuite: "catalog-prd-legacy-semantics-v1", TestEvidenceSHA256: stringsOf("c"),
		SemanticChecks: []string{"current-database-read-without-migration", "retained-history-unchanged", "launcher-resolves-preserved-legacy-binaries", "queued-wrapper-resolves-preserved-legacy-binaries", "rollback-preserves-current-database"},
		TestedAt:       time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := HostProfile{LegacyCompatibilityFile: path}
	if _, err := legacyCompatibilityInputSHA(profile); err != nil {
		t.Fatalf("valid canonical owner-only compatibility input rejected: %v", err)
	}
}

func stringsOf(char string) string { return strings.Repeat(char, 64) }

func TestPromotionCapacityKeepsOperatingBufferAndPRDReserveSeparate(t *testing.T) {
	if got := promotionRequiredCapacity("dev", 0); got != 500_000_000 {
		t.Fatalf("Dev minimum capacity=%d", got)
	}
	if got := promotionRequiredCapacity("qas", 600_000_000); got != 600_000_000 {
		t.Fatalf("QAS custom operating buffer=%d", got)
	}
	if got := promotionRequiredCapacity("prd", 0); got != 567_108_864 {
		t.Fatalf("PRD operating buffer plus selector reserve=%d", got)
	}
}

func TestDecodePromotionAdmissionMatchesCatalogCohortDiscoverEnvelope(t *testing.T) {
	const operationID = "c02b2c01b3504c067de9d3b77605b87e"
	tests := []struct {
		name    string
		view    any
		wantErr bool
	}{
		{
			name: "unheld lane",
			view: map[string]any{"host_id": "linux-host", "lane": "dev", "admission_held": false},
		},
		{
			name: "held lane includes receipt",
			view: map[string]any{"host_id": "linux-host", "lane": "dev", "admission_held": true,
				"operation_id": operationID, "receipt": laneinstall.CatalogCohortReceipt{
					OperationID: operationID, Lane: "dev",
				}},
		},
		{
			name: "held lane without receipt",
			view: map[string]any{"host_id": "linux-host", "lane": "dev", "admission_held": true,
				"operation_id": operationID},
			wantErr: true,
		},
		{
			name: "receipt operation mismatch",
			view: map[string]any{"host_id": "linux-host", "lane": "dev", "admission_held": true,
				"operation_id": operationID, "receipt": laneinstall.CatalogCohortReceipt{
					OperationID: "11111111111111111111111111111111", Lane: "dev",
				}},
			wantErr: true,
		},
		{
			name:    "host mismatch",
			view:    map[string]any{"host_id": "sandbox-host", "lane": "dev", "admission_held": false},
			wantErr: true,
		},
		{
			name:    "lane mismatch",
			view:    map[string]any{"host_id": "linux-host", "lane": "qas", "admission_held": false},
			wantErr: true,
		},
		{
			name: "unheld lane with receipt",
			view: map[string]any{"host_id": "linux-host", "lane": "dev", "admission_held": false,
				"receipt": laneinstall.CatalogCohortReceipt{OperationID: operationID, Lane: "dev"}},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.view)
			if err != nil {
				t.Fatal(err)
			}
			_, err = decodePromotionAdmission(data, "linux-host", "dev")
			if (err != nil) != tc.wantErr {
				t.Fatalf("decodePromotionAdmission error=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}

	// This is the public catalog-cohort discover envelope: its host, lane and
	// optional full receipt must be accepted by the promotion controller's
	// strict decoder rather than misclassified as a corrupt record.
	if _, err := decodePromotionAdmission([]byte(`{"host_id":"linux-host","lane":"dev","admission_held":true,"operation_id":"`+operationID+`","receipt":{"operation_id":"`+operationID+`","lane":"dev"}}`), "linux-host", "dev"); err != nil {
		t.Fatalf("catalog-cohort discover envelope rejected: %v", err)
	}
}

func TestPeerLaneDigestIgnoresSharedFilesystemHeadroom(t *testing.T) {
	snapshot := promotionLaneSnapshot{Config: "config", Schema: 40, AvailableBytes: 2_000_000_000, RequiredBytes: 500_000_000}
	first := promotionPeerSnapshotDigest(snapshot)
	snapshot.AvailableBytes = 1_500_000_000
	second := promotionPeerSnapshotDigest(snapshot)
	if first != second {
		t.Fatal("filesystem capacity movement was mistaken for a peer-lane identity change")
	}
	snapshot.Config = "changed"
	if promotionPeerSnapshotDigest(snapshot) == first {
		t.Fatal("peer-lane configuration drift was omitted from the isolation digest")
	}
}

func TestPromotionProbeRequestMustRemainFixedAndFiveSecondBounded(t *testing.T) {
	cases := []struct {
		name      string
		transport string
		host      string
		body      map[string]any
		wantErr   bool
	}{
		{name: "direct", transport: "direct-mtls", host: "linux-host", body: map[string]any{
			"environment": "legacy-prd-worker", "execution_target": map[string]any{"kind": "remote", "profile": "linux-host"},
			"source": map[string]any{"mode": "empty"}, "script": "printf 'catalog release maintenance probe\\n'", "timeout_seconds": 5,
		}},
		{name: "queued", transport: "queued-ssh", host: "factory-sandbox", body: map[string]any{
			"environment": "factory-prod", "execution_target": map[string]any{"kind": "remote", "profile": "factory-sandbox"},
			"source": map[string]any{"mode": "empty"}, "script": "printf 'catalog release maintenance probe\\n'",
			"limits": map[string]int{"command_timeout_seconds": 5},
		}},
		{name: "wrong timeout", transport: "direct-mtls", host: "linux-host", body: map[string]any{
			"environment": "linux-host-dev", "execution_target": map[string]any{"kind": "remote", "profile": "linux-host"},
			"source": map[string]any{"mode": "empty"}, "script": "printf 'catalog release maintenance probe\\n'", "timeout_seconds": 6,
		}, wantErr: true},
		{name: "wrong target", transport: "queued-ssh", host: "factory-sandbox", body: map[string]any{
			"environment": "factory-prod", "execution_target": map[string]any{"kind": "remote", "profile": "sandbox-host"},
			"source": map[string]any{"mode": "empty"}, "script": "printf 'catalog release maintenance probe\\n'",
			"limits": map[string]int{"command_timeout_seconds": 5},
		}, wantErr: true},
		{name: "caller script", transport: "mac-local", host: "mac-control-plane", body: map[string]any{
			"environment": "mac-workstation-prd", "execution_target": map[string]any{"kind": "local", "profile": "mac-workstation"},
			"source": map[string]any{"mode": "empty"}, "script": "id", "timeout_seconds": 5,
		}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(body)
			prepared := promotionPreparedProbe{HostID: tc.host, Transport: tc.transport, Environment: tc.body["environment"].(string),
				RequestSHA256: hex.EncodeToString(sum[:]), RequestBody: body}
			err = validatePromotionProbeRequest(prepared)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validatePromotionProbeRequest error=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
