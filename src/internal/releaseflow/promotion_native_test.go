package releaseflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/laneinstall"
	"remote-session-runner/src/internal/store"
)

func TestPromotionProbeControllerIDSupportsLegacyAndKeepsV3Strict(t *testing.T) {
	legacy := config.RemoteHostProfile{
		QueuedBridge:   &config.QueuedBridgeProfile{Host: "129.151.232.40", Port: 22},
		DirectEndpoint: &config.DirectEndpointProfile{Name: "linux-poc", Endpoint: "https://129.151.232.40:8443"},
	}
	for _, schema := range []int{config.VersionV1, config.VersionV2} {
		got, err := promotionProbeControllerID(schema, legacy, "direct-mtls")
		if err != nil || got != config.MacAccount {
			t.Errorf("legacy schema %d direct-mTLS controller=%q, err=%v", schema, got, err)
		}
	}
	got, err := promotionProbeControllerID(config.VersionV2, legacy, "queued-ssh")
	if err != nil || got != config.MacAccount {
		t.Fatalf("legacy queued-SSH controller=%q, err=%v", got, err)
	}

	v3 := legacy
	v3.DirectEndpoint = &config.DirectEndpointProfile{
		Name: "linux-prd", Endpoint: "https://129.151.232.40:8443",
		ControllerType: string(domain.ControllerTypeDirectMTLS), ControllerID: "runner-controller-prd-linux-host",
	}
	got, err = promotionProbeControllerID(config.VersionV3, v3, "direct-mtls")
	if err != nil || got != v3.DirectEndpoint.ControllerID {
		t.Fatalf("V3 explicit direct-mTLS controller=%q, err=%v", got, err)
	}
	v3.DirectEndpoint.ControllerID = ""
	if _, err := promotionProbeControllerID(config.VersionV3, v3, "direct-mtls"); err == nil {
		t.Fatal("V3 direct-mTLS controller fell back to the legacy Mac account")
	}
	if _, err := promotionProbeControllerID(config.VersionV2, config.RemoteHostProfile{QueuedBridge: legacy.QueuedBridge}, "direct-mtls"); err == nil {
		t.Fatal("legacy direct-mTLS controller was accepted without its configured endpoint")
	}
	if _, err := promotionProbeControllerID(config.VersionV2, config.RemoteHostProfile{DirectEndpoint: legacy.DirectEndpoint}, "queued-ssh"); err == nil {
		t.Fatal("queued-SSH controller was accepted without its configured bridge")
	}
}

func TestStartReleasedCohortRestartsActiveServicesAfterAcceptance(t *testing.T) {
	release := testRelease()
	const operationID = "dea80fa695a606e64985f0b2b53ebeb4"
	receipt := laneinstall.CatalogCohortReceipt{OperationID: operationID, Lane: config.ReleaseLaneDev,
		ReleaseID: release.ID, ReleaseSHA256: release.Digest, Stage: laneinstall.CatalogCohortReleased}
	active := make(map[string]bool, len(HostOrder))
	for _, host := range HostOrder {
		active[host] = true // acceptance left each server running under the hold
	}
	var actions []string
	backend := NativePromotionBackend{OperatingBuffer: defaultPromotionOperatingBuffer}
	backend.capacityReader = func(_ context.Context, _ string, _ string, snapshot *promotionLaneSnapshot) error {
		snapshot.AvailableBytes = 1_000_000_000
		snapshot.RequiredBytes = defaultPromotionOperatingBuffer
		return nil
	}
	backend.laneCommandRunner = func(_ context.Context, host string, args []string, _ []byte) ([]byte, error) {
		argValue := func(name string) string {
			for i := 0; i+1 < len(args); i++ {
				if args[i] == name {
					return args[i+1]
				}
			}
			return ""
		}
		switch args[0] {
		case "catalog-cohort":
			switch argValue("--action") {
			case "inspect":
				return json.Marshal(promotionReceiptView{Present: true, Receipt: receipt})
			case "discover":
				return json.Marshal(promotionAdmissionView{HostID: host, Lane: config.ReleaseLaneDev})
			default:
				t.Fatalf("unexpected cohort action: %v", args)
			}
		case "catalog-status":
			bundle := release.Bundles[host]
			status := laneinstall.CatalogSelectorStatus{HostID: host, Lane: config.ReleaseLaneDev, Kind: "catalog",
				Release: &laneinstall.CatalogReleaseStatus{Identity: laneinstall.CatalogReleaseIdentity{
					ReleaseID: release.ID, ReleaseSHA256: release.Digest, HostID: host, BundleSHA256: bundle.SHA256,
				}}}
			return json.Marshal(status)
		case "catalog-work-inventory":
			bundle := release.Bundles[host]
			report := laneinstall.CatalogWorkInventoryReport{HostID: host, Lane: config.ReleaseLaneDev,
				ServiceActive: active[host], SelectorTarget: "/sealed/" + release.ID + "/" + host,
				BundleSHA256: bundle.SHA256, ConfigurationSHA256: stringsOf("b"), DatabaseSchema: 40,
				WorkInventoryEmpty: true}
			return json.Marshal(report)
		case "stop":
			if !active[host] {
				t.Fatalf("attempted to stop already-stopped %s", host)
			}
			active[host] = false
			actions = append(actions, host+":stop")
			return []byte("{}"), nil
		case "start":
			if active[host] {
				t.Fatalf("attempted to start already-active %s", host)
			}
			active[host] = true
			actions = append(actions, host+":start")
			return []byte("{}"), nil
		default:
			t.Fatalf("unexpected host command: %v", args)
		}
		return nil, ErrInvalidInput
	}

	op := Operation{OperationID: operationID, Lane: "dev", Stage: StageAccepted,
		CohortOperationID: operationID, Release: release}
	if err := backend.startReleasedCohort(context.Background(), op); err != nil {
		t.Fatalf("startReleasedCohort failed: %v", err)
	}
	want := make([]string, 0, len(HostOrder)*2)
	for _, host := range HostOrder {
		want = append(want, host+":stop", host+":start")
		if !active[host] {
			t.Errorf("%s was not active after the released-cohort restart", host)
		}
	}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("released cohort restart order = %v, want %v", actions, want)
	}
}

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

func TestPRDApprovalPackageDigestIgnoresOnlyVolatileFreeSpace(t *testing.T) {
	release := testRelease()
	baselines := make([]HostObservation, len(HostOrder))
	for index, host := range HostOrder {
		baselines[index] = HostObservation{
			HostID: host, Lane: "prd", SelectedReleaseID: "cat-prior", SelectedBundleSHA: stringsOf("a"),
			ConfigurationSHA: stringsOf("b"), SchemaVersion: 40, WorkInventorySHA: stringsOf("c"),
			WorkInventoryEmpty: true, AvailableBytes: 2_000_000_000, RequiredBytes: 567_108_864,
		}
	}
	baselineDigest := prdBackupRecoveryPackageDigest(release, baselines, defaultPromotionOperatingBuffer)
	changedCapacity := append([]HostObservation(nil), baselines...)
	for index := range changedCapacity {
		changedCapacity[index].AvailableBytes -= 100_000_000
	}
	if got := prdBackupRecoveryPackageDigest(release, changedCapacity, defaultPromotionOperatingBuffer); got != baselineDigest {
		t.Fatal("ordinary free-space changes invalidated the release-bound approval package")
	}
	changedConfig := append([]HostObservation(nil), baselines...)
	changedConfig[0].ConfigurationSHA = stringsOf("d")
	if got := prdBackupRecoveryPackageDigest(release, changedConfig, defaultPromotionOperatingBuffer); got == baselineDigest {
		t.Fatal("configuration drift did not change the approval package digest")
	}
	changedWork := append([]HostObservation(nil), baselines...)
	changedWork[0].WorkInventorySHA = stringsOf("e")
	if got := prdBackupRecoveryPackageDigest(release, changedWork, defaultPromotionOperatingBuffer); got == baselineDigest {
		t.Fatal("work-inventory drift did not change the approval package digest")
	}
	changedPredecessor := append([]HostObservation(nil), baselines...)
	changedPredecessor[0].SelectedBundleSHA = stringsOf("f")
	if got := prdBackupRecoveryPackageDigest(release, changedPredecessor, defaultPromotionOperatingBuffer); got == baselineDigest {
		t.Fatal("predecessor bundle drift did not change the approval package digest")
	}
	if got := prdBackupRecoveryPackageDigest(release, baselines, defaultPromotionOperatingBuffer+1); got == baselineDigest {
		t.Fatal("operating-buffer policy change did not change the approval package digest")
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

func TestPromotionProbeObservationDiagnosticsAreExactBoundedAndSanitized(t *testing.T) {
	if _, got := decodePromotionProbeOutcome(nil, false, "linux-host", "operation", "direct-mtls", "job-12345678901234567890123456789012", true); got != "empty stdout" {
		t.Fatalf("empty helper result decode error=%q, want exact empty stdout classification", got)
	}
	if _, got := decodePromotionProbeOutcome([]byte(`{"resource":`), false, "linux-host", "operation", "direct-mtls", "", false); got != "stdout is not valid JSON" {
		t.Fatalf("invalid helper result decode error=%q", got)
	}
	if _, got := decodePromotionProbeOutcome([]byte(`{}`), true, "linux-host", "operation", "direct-mtls", "", false); got != "stdout exceeded 8 MiB bound" {
		t.Fatalf("truncated helper result decode error=%q", got)
	}

	stderr := []byte("accepted probe status read returned HTTP 503 (response_code maintenance_admission_closed)\nexit status 1\n")
	if got, want := sanitizedProbeHelperStderr(stderr, false), "accepted probe status read returned HTTP 503 (response_code maintenance_admission_closed)"; got != want {
		t.Fatalf("sanitized helper stderr=%q, want %q", got, want)
	}
	if got := sanitizedProbeHelperStderr([]byte("token=do-not-disclose\n"), false); got != "unrecognized helper stderr; content omitted" {
		t.Fatalf("unrecognized helper stderr was not suppressed: %q", got)
	}
	if got := sanitizedProbeHelperStderr(stderr, true); got != "stderr exceeded 4 KiB bound; content omitted" {
		t.Fatalf("truncated helper stderr was not bounded: %q", got)
	}

	bounded := &boundedProcessOutput{limit: 4}
	if n, err := bounded.Write([]byte("0123456789")); err != nil || n != 10 || bounded.String() != "0123" || !bounded.truncated {
		t.Fatalf("bounded subprocess capture n=%d err=%v bytes=%q truncated=%v", n, err, bounded.String(), bounded.truncated)
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
	ready.CreatedAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
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
	accepted.CreatedAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
	if action, err := classify(accepted); err != nil || action != "observe" {
		t.Fatalf("accepted scope disposition=%q err=%v", action, err)
	}
	accepted.ControllerID = "other-controller"
	if _, err := classify(accepted); err == nil {
		t.Fatal("accepted scope with controller drift was allowed to be observed")
	}
}

func TestExactPRDDirectProbeGetsOnlyOneRetryAfterSupportedLostDisposition(t *testing.T) {
	now := time.Now().UTC()
	base := promotionProbeScopeObservation{
		HostID: "linux-host", Lane: "prd", Transport: "direct-mtls", OperationID: "63471d41af07b7afaf577d6a0989cc03",
		ControllerType: "direct_mtls", ControllerID: "tomasz.walczuk", Environment: "linux-dev",
		ConfigurationSHA256: strings.Repeat("a", 64), IdempotencyKey: "catalog-probe-63471d41af07b7afaf577d6a0989cc03-linux-host",
		RequestSHA256: strings.Repeat("b", 64), CreatedAt: now.Add(-3 * time.Minute).Format(time.RFC3339Nano),
		ExpiresAt: now.Add(-time.Minute).Format(time.RFC3339Nano), State: "accepted", JobID: "job-7fae7172e801f2e22e7d6fce6014403d",
	}
	if got, err := existingProbeDisposition(base, "linux-host", "prd", "direct-mtls", base.OperationID, base.ControllerID, now); err != nil || got != "retry" {
		t.Fatalf("exact retained base scope disposition=%q err=%v, want one guarded retry", got, err)
	}
	unexpired := base
	unexpired.CreatedAt = now.Add(-30 * time.Second).Format(time.RFC3339Nano)
	unexpired.ExpiresAt = now.Add(90 * time.Second).Format(time.RFC3339Nano)
	if got, err := existingProbeDisposition(unexpired, "linux-host", "prd", "direct-mtls", base.OperationID, base.ControllerID, now); err != nil || got != "observe" {
		t.Fatalf("unexpired exact accepted scope disposition=%q err=%v, want read-only observe", got, err)
	}

	wrongJob := base
	wrongJob.JobID = "job-00000000000000000000000000000000"
	if _, err := existingProbeDisposition(wrongJob, "linux-host", "prd", "direct-mtls", base.OperationID, base.ControllerID, now); !errors.Is(err, ErrOperationCorrupted) {
		t.Fatalf("unexpected retained job was not rejected: %v", err)
	}

	retry := base
	retry.IdempotencyKey += "-retry-1"
	retry.JobID = "job-11111111111111111111111111111111"
	if got, err := existingProbeDisposition(retry, "linux-host", "prd", "direct-mtls", base.OperationID, base.ControllerID, now); err != nil || got != "observe" {
		t.Fatalf("accepted one-time retry disposition=%q err=%v, want observe only", got, err)
	}

	readyRetry := retry
	readyRetry.State = "ready"
	readyRetry.JobID = ""
	readyRetry.CreatedAt = now.Add(-3 * time.Minute).Format(time.RFC3339Nano)
	readyRetry.ExpiresAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
	if got, err := existingProbeDisposition(readyRetry, "linux-host", "prd", "direct-mtls", base.OperationID, base.ControllerID, now); err != nil || got != "retry" {
		t.Fatalf("expired unused retry scope disposition=%q err=%v, want guarded token renewal", got, err)
	}

	readyRetry.IdempotencyKey += "-again"
	if _, err := existingProbeDisposition(readyRetry, "linux-host", "prd", "direct-mtls", base.OperationID, base.ControllerID, now); !errors.Is(err, ErrOperationCorrupted) {
		t.Fatalf("second retry identity was accepted: %v", err)
	}
}

func TestRecoverAcceptedProbeFromExactControllerDecodeFailureWithoutReplay(t *testing.T) {
	op, scope := acceptedProbeRecoveryFixture(t)
	var calls [][]string
	backend := NativePromotionBackend{laneCommandRunner: func(_ context.Context, host string, args []string, _ []byte) ([]byte, error) {
		if host != "linux-host" {
			t.Fatalf("scope observation used unexpected host %q", host)
		}
		calls = append(calls, append([]string(nil), args...))
		if len(args) < 2 || args[0] != "catalog-probe-prepare" || args[len(args)-1] != "--observe-existing" {
			t.Fatalf("unexpected host-side command; consumed capability must only be observed: %v", args)
		}
		return json.Marshal(scope)
	}}
	got, err := backend.runProbe(context.Background(), op, "linux-host", "direct-mtls", "runner-controller-dev-linux-host", "/unused/config", "")
	if err != nil {
		t.Fatalf("accepted probe outcome recovery failed: %v", err)
	}
	if len(calls) != 1 || got.JobID != scope.JobID || got.FinalEventSequence != nil || got.AcceptedAt != "" ||
		got.AcceptanceEvidence == nil || got.AcceptanceEvidence.ControllerFailureEventSequence != op.Events[len(op.Events)-1].Sequence ||
		got.AcceptanceEvidence.ControllerFailureEventSHA256 != op.Events[len(op.Events)-1].SHA256 || !promotionProbeOutcomeAccepted(got) {
		t.Fatalf("recovered outcome omitted bounded proof or claimed unavailable fields: calls=%v outcome=%+v", calls, got)
	}
}

func TestRecoverAcceptedProbeAfterSafeObservationFailureDuringWorkflowResume(t *testing.T) {
	op, scope := acceptedProbeRecoveryFixture(t)
	const observationError = "linux-host direct-mtls functional probe: existing accepted probe could not be safely observed (exit=exit status 1): release workflow record is invalid or corrupted"
	if err := AppendEvent(&op, "acceptance", "", false, "failed", map[string]string{"error": observationError},
		time.Date(2026, 10, 24, 22, 23, 1, 362164000, time.UTC)); err != nil {
		t.Fatal(err)
	}
	// Promote resets a blocked operation to Prepared before AcceptCohort retries.
	// Recovery must use the original hash-chained helper-exit event, not assume
	// it remains the latest event or that the workflow stage is still Blocked.
	op.Stage = StagePrepared
	op.LastError = ""

	var calls [][]string
	backend := NativePromotionBackend{laneCommandRunner: func(_ context.Context, host string, args []string, _ []byte) ([]byte, error) {
		if host != "linux-host" {
			t.Fatalf("scope observation used unexpected host %q", host)
		}
		calls = append(calls, append([]string(nil), args...))
		if len(args) < 2 || args[0] != "catalog-probe-prepare" || args[len(args)-1] != "--observe-existing" {
			t.Fatalf("unexpected host-side command; accepted capability must only be observed: %v", args)
		}
		return json.Marshal(scope)
	}}
	got, err := backend.runProbe(context.Background(), op, "linux-host", "direct-mtls", "runner-controller-dev-linux-host", "/unused/config", "")
	if err != nil {
		t.Fatalf("accepted probe recovery after status-only failure failed: %v", err)
	}
	original := op.Events[0]
	if len(calls) != 1 || got.JobID != scope.JobID || got.FinalEventSequence != nil || got.AcceptedAt != "" ||
		got.AcceptanceEvidence == nil || got.AcceptanceEvidence.ControllerFailureEventSequence != original.Sequence ||
		got.AcceptanceEvidence.ControllerFailureEventSHA256 != original.SHA256 || !promotionProbeOutcomeAccepted(got) {
		t.Fatalf("recovery did not bind to the original helper-exit proof without replay: calls=%v outcome=%+v", calls, got)
	}
}

func TestAcceptedProbeObserveFailureRecognizesBoundedDiagnosticOnly(t *testing.T) {
	good := "linux-host direct-mtls functional probe: existing accepted probe observation result_decode=empty stdout; " +
		"helper_stderr=accepted probe status read returned HTTP 503 (response_code maintenance_admission_closed); " +
		"helper_exit_code=1: " + ErrOperationCorrupted.Error()
	if !acceptedProbeObserveFailure(good, "linux-host", "direct-mtls") {
		t.Fatal("bounded read-only status failure was not recognized")
	}
	unsafe := strings.Replace(good, "maintenance_admission_closed", "secret-token", 1)
	if acceptedProbeObserveFailure(unsafe, "linux-host", "direct-mtls") {
		t.Fatal("unrecognized helper stderr was accepted as a safe observation failure")
	}
	unknownDecode := strings.Replace(good, "empty stdout", "arbitrary decode failure", 1)
	if acceptedProbeObserveFailure(unknownDecode, "linux-host", "direct-mtls") {
		t.Fatal("unknown result decode category was accepted")
	}
	wrongExit := strings.Replace(good, "helper_exit_code=1", "helper_exit_code=failed", 1)
	if acceptedProbeObserveFailure(wrongExit, "linux-host", "direct-mtls") {
		t.Fatal("non-numeric helper exit code was accepted")
	}
}

func TestRecoverAcceptedProbeRejectsInconsistentEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Operation, *promotionProbeScopeObservation)
	}{
		{name: "invalid job identity", mutate: func(_ *Operation, scope *promotionProbeScopeObservation) {
			scope.JobID = "not-a-job-id"
		}},
		{name: "configuration drift", mutate: func(_ *Operation, scope *promotionProbeScopeObservation) { scope.ConfigurationSHA256 = stringsOf("d") }},
		{name: "operation mismatch", mutate: func(_ *Operation, scope *promotionProbeScopeObservation) { scope.OperationID = strings.Repeat("e", 32) }},
		{name: "scope created after failure", mutate: func(_ *Operation, scope *promotionProbeScopeObservation) { scope.CreatedAt = "2026-10-24T21:05:41Z" }},
		{name: "event after expiry", mutate: func(op *Operation, _ *promotionProbeScopeObservation) {
			at := time.Date(2026, 10, 24, 21, 8, 0, 0, time.UTC)
			op.Events = nil
			if err := AppendEvent(op, "acceptance", "", false, "failed", map[string]string{"error": op.LastError}, at); err != nil {
				panic(err)
			}
		}},
		{name: "newer operation event", mutate: func(op *Operation, _ *promotionProbeScopeObservation) {
			if err := AppendEvent(op, "status", "", false, "observed", map[string]string{"state": "blocked"}, time.Date(2026, 10, 24, 21, 6, 0, 0, time.UTC)); err != nil {
				panic(err)
			}
		}},
		{name: "unrelated later acceptance failure", mutate: func(op *Operation, _ *promotionProbeScopeObservation) {
			if err := AppendEvent(op, "acceptance", "", false, "failed", map[string]string{"error": "sandbox-host queued-ssh functional probe: unrelated failure"}, time.Date(2026, 10, 24, 21, 6, 0, 0, time.UTC)); err != nil {
				panic(err)
			}
		}},
		{name: "later successful acceptance", mutate: func(op *Operation, _ *promotionProbeScopeObservation) {
			if err := AppendEvent(op, "acceptance", "", false, "passed", map[string]string{"profile": "catalog-functional-v1"}, time.Date(2026, 10, 24, 21, 6, 0, 0, time.UTC)); err != nil {
				panic(err)
			}
		}},
		{name: "different last error", mutate: func(op *Operation, _ *promotionProbeScopeObservation) { op.LastError = "unrelated failure" }},
		{name: "mismatched event evidence", mutate: func(op *Operation, _ *promotionProbeScopeObservation) {
			op.Events = nil
			if err := AppendEvent(op, "acceptance", "", false, "failed", map[string]string{"error": "unrelated failure"}, time.Date(2026, 10, 24, 21, 5, 40, 0, time.UTC)); err != nil {
				panic(err)
			}
		}},
		{name: "event chain tampering", mutate: func(op *Operation, _ *promotionProbeScopeObservation) {
			op.Events[len(op.Events)-1].SHA256 = stringsOf("f")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			op, scope := acceptedProbeRecoveryFixture(t)
			tc.mutate(&op, &scope)
			if _, ok := recoverAcceptedProbeFromOperation(op, scope, "linux-host", "direct-mtls", "runner-controller-dev-linux-host"); ok {
				t.Fatal("inconsistent accepted-scope evidence recovered as a pass")
			}
		})
	}
}

func TestPromotionProbeAcceptanceRequiresSequenceOrExactRecoveryProof(t *testing.T) {
	zero := 0
	seq := int64(5)
	base := promotionProbeOutcome{Phase: "complete", CommandState: "succeeded", ExitCode: &zero, OutputComplete: true, TeardownState: "closed"}
	if promotionProbeOutcomeAccepted(base) {
		t.Fatal("ordinary result without a final event sequence passed")
	}
	base.FinalEventSequence = &seq
	if !promotionProbeOutcomeAccepted(base) {
		t.Fatal("complete ordinary result with a positive final sequence failed")
	}
	base.FinalEventSequence = nil
	base.Transport, base.HostID, base.OperationID = "direct-mtls", "linux-host", strings.Repeat("a", 32)
	base.JobID, base.ObservedExisting = "job-705974bc8f474d77898670837be15b61", true
	base.ConfigurationSHA, base.RequestSHA256, base.RequestedTimeout = stringsOf("b"), stringsOf("c"), 5
	base.AcceptanceEvidence = &promotionProbeAcceptanceEvidence{Method: acceptedProbeRecoveryMethod, HelperExitCode: 0,
		HelperContract: acceptedProbeHelperContract, ControllerFailureEventSequence: 1,
		ControllerFailureEventSHA256: stringsOf("a"), ControllerFailureEventAtUTC: "2026-10-24T21:05:40.97741Z",
		ScopeCreatedAt: "2026-10-24T21:05:35.923209659Z", ScopeExpiresAt: "2026-10-24T21:07:35.404263511Z",
		HelperVerifiedPositiveFinalSequence: true}
	if !promotionProbeOutcomeAccepted(base) {
		t.Fatal("bounded exit-zero recovery proof did not pass")
	}
	base.AcceptanceEvidence.HelperExitCode = 1
	if promotionProbeOutcomeAccepted(base) {
		t.Fatal("nonzero helper exit was accepted by recovery proof")
	}
	base.AcceptanceEvidence.HelperExitCode = 0
	base.AcceptanceEvidence.ScopeCreatedAt = "2026-10-24T21:07:36Z"
	if promotionProbeOutcomeAccepted(base) {
		t.Fatal("recovery proof with event outside scope lifetime was accepted")
	}
}

func acceptedProbeRecoveryFixture(t *testing.T) (Operation, promotionProbeScopeObservation) {
	t.Helper()
	const operationID = "dea80fa695a606e64985f0b2b53ebeb4"
	createdAt := time.Date(2026, 10, 24, 21, 5, 35, 923209659, time.UTC)
	expiresAt := time.Date(2026, 10, 24, 21, 7, 35, 404263511, time.UTC)
	eventAt := time.Date(2026, 10, 24, 21, 5, 40, 977410000, time.UTC)
	op, err := NewOperation(operationID, "dev", "all", testRelease(), "catalog-functional-v1", "", createdAt.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	op.Stage = StageBlocked
	for _, host := range HostOrder {
		configuration := stringsOf("b")
		if host != "linux-host" {
			configuration = digest([]byte("config-" + host))
		}
		op.PlanSnapshot.HostBaselines = append(op.PlanSnapshot.HostBaselines, HostObservation{HostID: host, Lane: "dev", ConfigurationSHA: configuration})
	}
	wantError := "linux-host direct-mtls functional probe: " + acceptedProbeDecodeFailure
	op.LastError = wantError
	if err := AppendEvent(&op, "acceptance", "", false, "failed", map[string]string{"error": wantError}, eventAt); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOperation(op); err != nil {
		t.Fatalf("fixture operation invalid: %v", err)
	}
	scope := promotionProbeScopeObservation{HostID: "linux-host", Lane: "dev", Transport: laneinstall.CatalogProbeDirectMTLS,
		OperationID: operationID, ControllerType: "direct_mtls", ControllerID: "runner-controller-dev-linux-host", Environment: "linux-host-dev",
		ConfigurationSHA256: stringsOf("b"), IdempotencyKey: "catalog-probe-" + operationID + "-linux-host",
		RequestSHA256: stringsOf("c"), CreatedAt: createdAt.Format(time.RFC3339Nano), ExpiresAt: expiresAt.Format(time.RFC3339Nano),
		State: "accepted", JobID: "job-705974bc8f474d77898670837be15b61"}
	return op, scope
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
