package runnerlocald

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

type macLocalProbeAuthorizerFixture struct {
	token, intentID, jobID string
}

func (g macLocalProbeAuthorizerFixture) AllowsReservedIntent(_ domain.ControllerType, _ string, token, intentID string, _ time.Time) bool {
	return token == g.token && intentID == g.intentID
}

func (g macLocalProbeAuthorizerFixture) AllowsReservedJobIntent(_ domain.ControllerType, _ string, token, intentID, jobID string, _ time.Time) bool {
	return token == g.token && intentID == g.intentID && jobID == g.jobID
}

func (g macLocalProbeAuthorizerFixture) ValidateFixedProbeRequestForController(environment, targetKind, targetProfile, sourceMode, script string,
	timeoutSeconds int64, controllerType domain.ControllerType, controllerID string) error {
	if environment != "mac-workstation-dev" || targetKind != "local" || targetProfile != "mac-workstation" || sourceMode != "empty" ||
		script != "printf 'catalog release maintenance probe\\n'" || timeoutSeconds != 5 ||
		controllerType != domain.ControllerTypeLocalUser || controllerID != "tomasz.walczuk" {
		return ErrIntentRequest
	}
	return nil
}

func TestMacLocalMaintenanceIntentRunsSynchronouslyAndSettles(t *testing.T) {
	ctx := context.Background()
	root := testfixture.New(t)
	db, err := store.Open(ctx, root.Path()+"/state/catalog-maintenance.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "mac-workstation-dev", HostClass: "mac", EffectiveAccount: "tomasz.walczuk",
		AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := execution.NewEnvironmentRegistry(environment)
	if err != nil {
		t.Fatal(err)
	}
	service, err := execution.NewExecutionService(authority, &p060FakeRuntime{}, registry, execution.RealClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const script = "printf 'catalog release maintenance probe\\n'"
	payload, err := json.Marshal(map[string]any{
		"operation": "run", "environment": "mac-workstation-dev",
		"execution_target": map[string]string{"kind": "local", "profile": "mac-workstation"},
		"source":           map[string]string{"mode": "empty"}, "script": script,
		"limits": map[string]int{"command_timeout_seconds": 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := authority.CreateLocalIntent(ctx, store.LocalIntentCreate{
		IntentID: "intent-0123456789abcdef0123456789abcdef", Operation: "run", ResourceID: "job-0123456789abcdef0123456789abcdef",
		JobID: "job-0123456789abcdef0123456789abcdef", SessionID: "sess-0123456789abcdef0123456789abcdef",
		CommandID: "cmd-0123456789abcdef0123456789abcdef", Target: target, Environment: "mac-workstation-dev",
		Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: "catalog-maintenance-local",
		PayloadJSON: canonical, ScriptBytes: []byte(script), DeliveryState: store.LocalIntentRecorded,
	})
	if err != nil {
		t.Fatal(err)
	}
	const token = "reserved-probe-token"
	authorizer := macLocalProbeAuthorizerFixture{token: token, intentID: string(intent.IntentID), jobID: string(intent.JobID)}
	server, err := NewPrivateServer(PrivateServerOptions{
		Authority: authority, Service: service, Owner: controller, SocketPath: p060SocketPath(t),
		MaintenanceAdmissionClosed: true, MaintenanceProbeGuard: authorizer,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(fmt.Sprintf(`{"intent_id":%q,"request_hash":%q}`, intent.IntentID, intent.RequestHash.String()))
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/accept-intent", strings.NewReader(string(body)))
	request.Header.Set("X-Runner-Catalog-Maintenance-Token", token)
	response := httptest.NewRecorder()
	server.serveHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("accept scoped Mac-local probe status=%d body=%s", response.Code, response.Body.String())
	}
	var accepted intentAcceptanceResponse
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.JobPhase != string(store.JobPhaseComplete) {
		t.Fatalf("Mac-local probe returned before execution and teardown: %+v", accepted)
	}
	job, err := authority.GetJobStatus(ctx, intent.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Phase != store.JobPhaseComplete || string(job.ScriptBytes) != script {
		t.Fatalf("durable maintenance job = %+v", job)
	}
	command, err := authority.GetCommand(ctx, intent.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if command.State != domain.CommandStateSucceeded || command.ExitCode == nil || *command.ExitCode != 0 {
		t.Fatalf("maintenance command = %+v", command)
	}
	wrongIntent := httptest.NewRequest(http.MethodPost, "/internal/v1/accept-intent", strings.NewReader(`{"intent_id":"intent-ffffffffffffffffffffffffffffffff","request_hash":"v1:`+strings.Repeat("0", 64)+`"}`))
	wrongIntent.Header.Set("X-Runner-Catalog-Maintenance-Token", token)
	wrongResponse := httptest.NewRecorder()
	server.serveHTTP(wrongResponse, wrongIntent)
	if wrongResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreserved intent during hold status=%d body=%s", wrongResponse.Code, wrongResponse.Body.String())
	}
}

func TestDecodeIntentPayloadReadsAndValidatesNestedLimits(t *testing.T) {
	decoded, err := decodeIntentPayload([]byte(`{"limits":{"command_timeout_seconds":5,"output_bytes_per_command":2048}}`))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Timeout != 5*time.Second || decoded.RequestedLimits.CommandTimeout != 5*time.Second || decoded.RequestedLimits.OutputBytesPerCommand != 2048 {
		t.Fatalf("nested request limits decoded as %+v", decoded)
	}
	for _, raw := range []string{
		`{"limits":{"command_timeout_seconds":5,"unexpected":1}}`,
		`{"command_timeout_seconds":6,"limits":{"command_timeout_seconds":5}}`,
		`{"limits":{"command_timeout_seconds":5.5}}`,
	} {
		if _, err := decodeIntentPayload([]byte(raw)); err == nil {
			t.Fatalf("invalid stored run limits accepted: %s", raw)
		}
	}
}
