package runnerd

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

const p112OtherPrincipalMapEntry = `  - uri_san: urn:remote-session-runner:controller:runner-other-direct
    controller_type: direct_mtls
    controller_id: other-controller
`

func TestP112D05I01DirectHTTPSMutationIdempotencyMatrixUsesMappedControllers(t *testing.T) {
	runtime := &p109Runtime{generation: "p112-matrix-generation"}
	service, authority, database := p112NewService(t, runtime)
	endpoint, controllerA, controllerB := p112StartMappedServer(t, service)

	createKey := "p112-create-key"
	status, body := p112Do(t, controllerA, http.MethodPost, endpoint+"/v1/sessions", []byte(p107CreateBody), createKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var sessionA directSessionAcceptance
	p112Decode(t, body, &sessionA)

	canonicalCreate := []byte(`{"source":{"mode":"empty"},"execution_target":{"profile":"linux-host","kind":"remote"},"environment":"linux-dev","limits":{},"policy":{}}`)
	status, body = p112Do(t, controllerA, http.MethodPost, endpoint+"/v1/sessions", canonicalCreate, createKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var createReplay directSessionAcceptance
	p112Decode(t, body, &createReplay)
	if createReplay.SessionID != sessionA.SessionID {
		t.Fatalf("create replay returned session %q, want original %q", createReplay.SessionID, sessionA.SessionID)
	}

	changedCreate := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"limits":{"command_timeout_seconds":60}}`)
	status, body = p112Do(t, controllerA, http.MethodPost, endpoint+"/v1/sessions", changedCreate, createKey)
	p112RequireError(t, status, body, http.StatusConflict, "idempotency_conflict")
	p112RequireSessionCounts(t, authority, 1, 1)

	// The same operation/key under a second certificate-mapped controller is
	// a separate namespace and must produce a separate owned resource.
	status, body = p112Do(t, controllerB, http.MethodPost, endpoint+"/v1/sessions", []byte(p107CreateBody), createKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var sessionB directSessionAcceptance
	p112Decode(t, body, &sessionB)
	if sessionB.SessionID == sessionA.SessionID {
		t.Fatalf("controller B received controller A's create result %q", sessionB.SessionID)
	}

	status, body = p112Do(t, controllerB, http.MethodGet, endpoint+"/v1/sessions/"+sessionA.SessionID, nil, "")
	p112RequireError(t, status, body, http.StatusForbidden, "controller_mismatch")
	status, body = p112Do(t, controllerA, http.MethodGet, endpoint+"/v1/sessions/"+sessionB.SessionID, nil, "")
	p112RequireError(t, status, body, http.StatusForbidden, "controller_mismatch")
	p112RequireSessionCounts(t, authority, 2, 2)

	// Submit: equivalent default timeout replays the same command; changed
	// script conflicts without scheduling another execution.
	submitPathA := endpoint + "/v1/sessions/" + sessionA.SessionID + "/commands"
	submitKey := "p112-submit-key"
	submitBody := []byte(`{"script":"printf p112-a"}`)
	status, body = p112Do(t, controllerA, http.MethodPost, submitPathA, submitBody, submitKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var commandA directCommandAcceptance
	p112Decode(t, body, &commandA)
	p108WaitForCommandState(t, authority, domain.CommandID(commandA.CommandID), domain.CommandStateSucceeded)

	status, body = p112Do(t, controllerA, http.MethodPost, submitPathA, []byte(`{"timeout_seconds":1800,"script":"printf p112-a"}`), submitKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var submitReplay directCommandAcceptance
	p112Decode(t, body, &submitReplay)
	if submitReplay.CommandID != commandA.CommandID {
		t.Fatalf("submit replay returned command %q, want original %q", submitReplay.CommandID, commandA.CommandID)
	}
	status, body = p112Do(t, controllerA, http.MethodPost, submitPathA, []byte(`{"script":"printf changed"}`), submitKey)
	p112RequireError(t, status, body, http.StatusConflict, "idempotency_conflict")
	status, body = p112Do(t, controllerB, http.MethodPost, submitPathA, submitBody, submitKey)
	p112RequireError(t, status, body, http.StatusForbidden, "controller_mismatch")
	commandsA, err := authority.ListSessionCommands(context.Background(), domain.SessionID(sessionA.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(commandsA) != 1 || runtime.commandCalls.Load() != 1 {
		t.Fatalf("after submit replay/conflict/denial, commands=%d runtime calls=%d; want one each", len(commandsA), runtime.commandCalls.Load())
	}

	// Reusing the same key in controller B's own session must not return A's
	// command, while access to A's command remains denied.
	submitPathB := endpoint + "/v1/sessions/" + sessionB.SessionID + "/commands"
	status, body = p112Do(t, controllerB, http.MethodPost, submitPathB, submitBody, submitKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var commandB directCommandAcceptance
	p112Decode(t, body, &commandB)
	p108WaitForCommandState(t, authority, domain.CommandID(commandB.CommandID), domain.CommandStateSucceeded)
	if commandB.CommandID == commandA.CommandID {
		t.Fatalf("controller B received controller A's command %q", commandA.CommandID)
	}
	status, body = p112Do(t, controllerB, http.MethodGet, endpoint+"/v1/commands/"+commandA.CommandID, nil, "")
	p112RequireError(t, status, body, http.StatusForbidden, "controller_mismatch")
	if runtime.commandCalls.Load() != 2 {
		t.Fatalf("controller-scoped submit calls=%d, want one command per controller", runtime.commandCalls.Load())
	}

	// Cancellation hashes include the command path. Replaying the original
	// command is inert; the same key on another command conflicts before that
	// second command can be cancelled.
	cancelOne := p109AcceptQueuedCommand(t, service, domain.SessionID(sessionA.SessionID), p107DirectController(t, "tomasz.walczuk"), "cmd-p112-cancel-one")
	cancelTwo := p109AcceptQueuedCommand(t, service, domain.SessionID(sessionA.SessionID), p107DirectController(t, "tomasz.walczuk"), "cmd-p112-cancel-two")
	cancelPathOne := endpoint + "/v1/commands/" + string(cancelOne) + "/cancel"
	cancelKey := "p112-cancel-key"
	status, body = p112Do(t, controllerA, http.MethodPost, cancelPathOne, nil, cancelKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	status, body = p112Do(t, controllerA, http.MethodPost, cancelPathOne, nil, cancelKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	status, body = p112Do(t, controllerA, http.MethodPost, endpoint+"/v1/commands/"+string(cancelTwo)+"/cancel", nil, cancelKey)
	p112RequireError(t, status, body, http.StatusConflict, "idempotency_conflict")
	status, body = p112Do(t, controllerB, http.MethodPost, cancelPathOne, nil, cancelKey)
	p112RequireError(t, status, body, http.StatusForbidden, "controller_mismatch")
	storedCancelOne, err := authority.GetCommand(context.Background(), cancelOne)
	if err != nil {
		t.Fatal(err)
	}
	storedCancelTwo, err := authority.GetCommand(context.Background(), cancelTwo)
	if err != nil {
		t.Fatal(err)
	}
	if storedCancelOne.State != domain.CommandStateCancelled || storedCancelTwo.State != domain.CommandStateQueued || runtime.cancelCalls.Load() != 0 {
		t.Fatalf("cancel matrix affected wrong resource: first=%s second=%s runtime cancels=%d", storedCancelOne.State, storedCancelTwo.State, runtime.cancelCalls.Load())
	}
	commandsA, err = authority.ListSessionCommands(context.Background(), domain.SessionID(sessionA.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(commandsA) != 3 {
		t.Fatalf("cancel replay/conflict changed command count to %d, want three", len(commandsA))
	}

	// Close hashes include both the session path and normalized policy.
	status, body = p112Do(t, controllerA, http.MethodPost, endpoint+"/v1/sessions", []byte(p107CreateBody), "p112-close-target-key")
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var closeTarget directSessionAcceptance
	p112Decode(t, body, &closeTarget)
	p112RequireSessionCounts(t, authority, 3, 3)
	closePathA := endpoint + "/v1/sessions/" + sessionA.SessionID
	closeKey := "p112-close-key"
	status, body = p112Do(t, controllerA, http.MethodDelete, closePathA, nil, closeKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	status, body = p112Do(t, controllerA, http.MethodDelete, closePathA, []byte(`{"policy":"graceful"}`), closeKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	status, body = p112Do(t, controllerA, http.MethodDelete, closePathA, []byte(`{"policy":"drain"}`), closeKey)
	p112RequireError(t, status, body, http.StatusConflict, "idempotency_conflict")
	status, body = p112Do(t, controllerA, http.MethodDelete, endpoint+"/v1/sessions/"+closeTarget.SessionID, nil, closeKey)
	p112RequireError(t, status, body, http.StatusConflict, "idempotency_conflict")
	status, body = p112Do(t, controllerB, http.MethodDelete, closePathA, nil, closeKey)
	p112RequireError(t, status, body, http.StatusForbidden, "controller_mismatch")
	status, body = p112Do(t, controllerA, http.MethodGet, endpoint+"/v1/sessions/"+closeTarget.SessionID, nil, "")
	p112RequireStatus(t, status, body, http.StatusOK)
	var untouchedSession directSessionReadResponse
	p112Decode(t, body, &untouchedSession)
	if untouchedSession.Resource.SessionState != string(domain.SessionStateReady) || runtime.stopCalls.Load() != 1 {
		t.Fatalf("close replay/conflict affected another session: state=%s stop calls=%d", untouchedSession.Resource.SessionState, runtime.stopCalls.Load())
	}
	p112RequireSessionCounts(t, authority, 3, 2)

	// Run: a canonical formatting replay reuses the job tuple; changed script
	// conflicts without a second command or teardown. Controller B has its own
	// scoped key binding and receives a distinct job tuple.
	runKey := "p112-run-key"
	runPath := endpoint + "/v1/jobs"
	runBody := []byte(p110RunBody)
	commandCallsBeforeRun := runtime.commandCalls.Load()
	stopCallsBeforeRun := runtime.stopCalls.Load()
	status, body = p112Do(t, controllerA, http.MethodPost, runPath, runBody, runKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var jobA directJobAcceptance
	p112Decode(t, body, &jobA)
	if runtime.commandCalls.Load() != commandCallsBeforeRun+1 || runtime.stopCalls.Load() != stopCallsBeforeRun+1 {
		t.Fatalf("first run side effects: command calls=%d stop calls=%d", runtime.commandCalls.Load(), runtime.stopCalls.Load())
	}
	p112RequireJobCount(t, database, 1)
	reorderedRunBody := []byte(`{"script":"printf p110","source":{"mode":"empty"},"execution_target":{"profile":"linux-host","kind":"remote"},"environment":"linux-dev"}`)
	status, body = p112Do(t, controllerA, http.MethodPost, runPath, reorderedRunBody, runKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var jobReplay directJobAcceptance
	p112Decode(t, body, &jobReplay)
	if jobReplay.JobID != jobA.JobID || jobReplay.SessionID != jobA.SessionID || jobReplay.CommandID != jobA.CommandID {
		t.Fatalf("run replay changed stable IDs: first=%#v replay=%#v", jobA, jobReplay)
	}
	changedRunBody := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"printf changed"}`)
	status, body = p112Do(t, controllerA, http.MethodPost, runPath, changedRunBody, runKey)
	p112RequireError(t, status, body, http.StatusConflict, "idempotency_conflict")
	if runtime.commandCalls.Load() != commandCallsBeforeRun+1 || runtime.stopCalls.Load() != stopCallsBeforeRun+1 {
		t.Fatalf("same-controller run replay/conflict repeated execution or close: command calls=%d stop calls=%d", runtime.commandCalls.Load(), runtime.stopCalls.Load())
	}
	p112RequireJobCount(t, database, 1)

	status, body = p112Do(t, controllerB, http.MethodPost, runPath, runBody, runKey)
	p112RequireStatus(t, status, body, http.StatusAccepted)
	var jobB directJobAcceptance
	p112Decode(t, body, &jobB)
	if jobB.JobID == jobA.JobID || jobB.SessionID == jobA.SessionID || jobB.CommandID == jobA.CommandID {
		t.Fatalf("controller B received controller A's one-off result: A=%#v B=%#v", jobA, jobB)
	}
	if runtime.commandCalls.Load() != commandCallsBeforeRun+2 || runtime.stopCalls.Load() != stopCallsBeforeRun+2 {
		t.Fatalf("controller-scoped run calls: command=%d stop=%d", runtime.commandCalls.Load(), runtime.stopCalls.Load())
	}
	p112RequireJobCount(t, database, 2)
	status, body = p112Do(t, controllerB, http.MethodGet, endpoint+"/v1/jobs/"+jobA.JobID, nil, "")
	p112RequireError(t, status, body, http.StatusForbidden, "controller_mismatch")
	status, body = p112Do(t, controllerA, http.MethodGet, endpoint+"/v1/jobs/"+jobB.JobID, nil, "")
	p112RequireError(t, status, body, http.StatusForbidden, "controller_mismatch")
}

func p112NewService(t *testing.T, runtime execution.SessionRuntime) (*execution.Service, *store.AuthorityStore, *sql.DB) {
	t.Helper()
	fixture := testfixture.New(t)
	db, err := store.Open(context.Background(), filepath.Join(fixture.Path(), "state", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controllerA := p107DirectController(t, "tomasz.walczuk")
	controllerB := p107DirectController(t, "other-controller")
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{
		Name: "linux-dev", HostClass: "Ubuntu Linux host", EffectiveAccount: "ubuntu",
		AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty},
		AllowedControllers: []domain.ControllerIdentity{controllerA, controllerB}, ServiceLimits: domain.DefaultServiceLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := execution.NewEnvironmentRegistry(environment)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	service, err := execution.NewExecutionService(authority, runtime, registry, execution.RealClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service, authority, db
}

func p112StartMappedServer(t *testing.T, service *execution.Service) (string, *http.Client, *http.Client) {
	t.Helper()
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	pki := newP106TestPKI(t)
	options := writeP106TestFiles(t, t.TempDir(), pki, validP106PrincipalMap+p112OtherPrincipalMapEntry)
	options.Handler = handler
	server, err := NewDirectHTTPSServer(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close P112 loopback HTTPS server: %v", err)
		}
		if err := <-serveResult; err != nil {
			t.Errorf("serve P112 loopback HTTPS server: %v", err)
		}
	})
	clientA := p106HTTPClient(pki.serverRoots, &pki.clientTLS, tls.VersionTLS13)
	clientB := p106HTTPClient(pki.serverRoots, &pki.otherClientTLS, tls.VersionTLS13)
	t.Cleanup(func() {
		clientA.CloseIdleConnections()
		clientB.CloseIdleConnections()
	})
	return "https://" + server.Addr(), clientA, clientB
}

func p112Do(t *testing.T, client *http.Client, method, url string, body []byte, key string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("P112 %s %s request: %v", method, url, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read P112 response body: %v", err)
	}
	return response.StatusCode, responseBody
}

func p112RequireStatus(t *testing.T, status int, body []byte, want int) {
	t.Helper()
	if status != want {
		t.Fatalf("P112 status = %d, want %d; body = %s", status, want, body)
	}
}

func p112RequireError(t *testing.T, status int, body []byte, wantStatus int, wantCode string) {
	t.Helper()
	p112RequireStatus(t, status, body, wantStatus)
	var apiError directAPIError
	p112Decode(t, body, &apiError)
	if apiError.Code != wantCode {
		t.Fatalf("P112 error code = %q, want %q; body = %s", apiError.Code, wantCode, body)
	}
}

func p112Decode(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode P112 response %q: %v", body, err)
	}
}

func p112RequireSessionCounts(t *testing.T, authority *store.AuthorityStore, wantSessions, wantReservations int) {
	t.Helper()
	sessions, err := authority.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reservations, err := authority.CountLiveSessionReservations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != wantSessions || reservations != wantReservations {
		t.Fatalf("P112 sessions=%d reservations=%d, want %d and %d", len(sessions), reservations, wantSessions, wantReservations)
	}
}

func p112RequireJobCount(t *testing.T, database *sql.DB, want int) {
	t.Helper()
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM exec_jobs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("P112 job rows=%d, want %d", count, want)
	}
}
