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

func TestPromotionPeerInventoryUsesCatalogReadOnlyCommandForEveryLane(t *testing.T) {
	for _, lane := range LaneOrder {
		args := promotionWorkInventoryArgs("linux-host", lane)
		if len(args) != 5 || args[0] != "catalog-work-inventory" || args[1] != "--host" || args[2] != "linux-host" ||
			args[3] != "--lane" || args[4] != lane {
			t.Errorf("lane %s work-inventory invocation = %v", lane, args)
		}
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

func TestPromotionProbeOutcomeDecodesCompletePublicHelperResult(t *testing.T) {
	const output = `{"transport":"direct-mtls","host_id":"linux-host","operation_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","accepted_at":"2026-10-24T21:05:35.923209659Z","job_id":"job-705974bc8f474d77898670837be15b61","session_id":"sess-11111111111111111111111111111111","command_id":"cmd-22222222222222222222222222222222","phase":"complete","command_state":"succeeded","exit_code":0,"final_event_sequence":7,"output_complete":true,"output_truncated":false,"teardown_state":"closed","http_status":202,"readiness_http_status":200}`
	var got promotionProbeOutcome
	if err := decodeStrictJSON([]byte(output), &got); err != nil {
		t.Fatalf("strict decoder rejected public catalog-probe-once result: %v", err)
	}
	if got.Transport != "direct-mtls" || got.JobID != "job-705974bc8f474d77898670837be15b61" || got.HTTPStatus != 202 ||
		got.ReadinessHTTPStatus != 200 || got.AcceptedAt == "" || got.CommandState != "succeeded" || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("public probe result fields were not retained: %+v", got)
	}
	if err := decodeStrictJSON([]byte(strings.TrimSuffix(output, "}")+`,"unreviewed":true}`), &got); err == nil {
		t.Fatal("strict probe result decoder accepted an unknown output field")
	}
}

func TestExistingPromotionProbeScopeNeverReplaysConsumedCapability(t *testing.T) {
	now := time.Date(2026, 10, 24, 21, 0, 0, 0, time.UTC)
	base := promotionProbeScopeObservation{
		HostID: "linux-host", Lane: "dev", Transport: laneinstall.CatalogProbeDirectMTLS,
		OperationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ControllerType: "direct_mtls", ControllerID: "tomasz.walczuk",
		Environment: "linux-dev", ConfigurationSHA256: stringsOf("b"), IdempotencyKey: "catalog-probe-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-linux-host",
		RequestSHA256: stringsOf("c"), State: "absent",
	}
	classify := func(value promotionProbeScopeObservation) (string, error) {
		return existingProbeDisposition(value, "linux-host", "dev", "direct-mtls", base.OperationID, "tomasz.walczuk", now)
	}
	if action, err := classify(base); err != nil || action != "prepare" {
		t.Fatalf("absent scope disposition=%q err=%v", action, err)
	}
	ready := base
	ready.State = "ready"
	ready.ExpiresAt = now.Add(time.Minute).Format(time.RFC3339Nano)
	if _, err := classify(ready); err == nil {
		t.Fatal("active unused capability was allowed to be replaced")
	}
	ready.ExpiresAt = now.Add(-time.Second).Format(time.RFC3339Nano)
	if action, err := classify(ready); err != nil || action != "prepare" {
		t.Fatalf("expired unused scope disposition=%q err=%v", action, err)
	}
	reserved := ready
	reserved.State = "reserved"
	reserved.ExpiresAt = now.Add(time.Minute).Format(time.RFC3339Nano)
	if _, err := classify(reserved); err == nil {
		t.Fatal("ambiguous reserved capability was allowed to be replayed")
	}
	accepted := reserved
	accepted.State = "accepted"
	accepted.JobID = "job-705974bc8f474d77898670837be15b61"
	if action, err := classify(accepted); err != nil || action != "observe" {
		t.Fatalf("accepted scope disposition=%q err=%v", action, err)
	}
	accepted.ControllerID = "other-controller"
	if _, err := classify(accepted); err == nil {
		t.Fatal("accepted scope with controller drift was allowed to be observed")
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

func TestPromotionPreparedProbeAcceptsAndValidatesPublicCommandControllerType(t *testing.T) {
	// Keep this wire fixture independent of promotionPreparedProbe so strict
	// decoding catches drift from runner-lane catalog-probe-prepare output.
	raw, err := json.Marshal(struct {
		HostID              string          `json:"host_id"`
		Lane                string          `json:"lane"`
		Transport           string          `json:"transport"`
		OperationID         string          `json:"operation_id"`
		ControllerType      string          `json:"controller_type"`
		ControllerID        string          `json:"controller_id"`
		Environment         string          `json:"environment"`
		ConfigurationSHA256 string          `json:"configuration_sha256"`
		IdempotencyKey      string          `json:"idempotency_key"`
		RequestSHA256       string          `json:"request_body_sha256"`
		RequestBody         json.RawMessage `json:"request_body"`
		ExpiresAt           string          `json:"expires_at"`
		Token               string          `json:"one_time_token"`
		State               string          `json:"state"`
	}{
		HostID: "linux-host", Lane: "dev", Transport: "direct-mtls", OperationID: strings.Repeat("a", 32),
		ControllerType: "direct_mtls", ControllerID: "tomasz.walczuk", Environment: "linux-host-dev",
		ConfigurationSHA256: stringsOf("b"), IdempotencyKey: "catalog-probe-test", RequestSHA256: stringsOf("c"),
		RequestBody: []byte(`{"script":"printf 'catalog release maintenance probe\\n'"}`),
		ExpiresAt:   "2026-10-24T20:00:00Z", Token: "one-time-token", State: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	var prepared promotionPreparedProbe
	if err := decodeStrictJSON(raw, &prepared); err != nil {
		t.Fatalf("decode public catalog-probe-prepare result: %v", err)
	}
	if err := validatePreparedPromotionProbe(prepared, "linux-host", "dev", "direct-mtls", strings.Repeat("a", 32), "tomasz.walczuk"); err != nil {
		t.Fatalf("valid direct-mTLS probe identity rejected: %v", err)
	}
	prepared.ControllerType = "queued_mac"
	if err := validatePreparedPromotionProbe(prepared, "linux-host", "dev", "direct-mtls", strings.Repeat("a", 32), "tomasz.walczuk"); err == nil {
		t.Fatal("controller type inconsistent with direct-mTLS transport accepted")
	}
}
