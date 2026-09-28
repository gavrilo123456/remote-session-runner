package runnerd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
)

func TestP109DirectCancelAndCloseReplayPolicyConflict(t *testing.T) {
	runtime := &p109Runtime{generation: "p109-lifecycle-generation"}
	service, authority := newP046Service(t, runtime)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")
	created := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "p109-create-key")
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var session directSessionAcceptance
	p107Decode(t, created, &session)

	commandID := p109AcceptQueuedCommand(t, service, domain.SessionID(session.SessionID), controller, "cmd-p109-cancel")
	cancelPath := "/v1/commands/" + string(commandID) + "/cancel"
	cancelled := p107Do(handler, controller, true, http.MethodPost, cancelPath, nil, "p109-cancel-key")
	if cancelled.Code != http.StatusAccepted {
		t.Fatalf("cancel status = %d, body = %s", cancelled.Code, cancelled.Body.String())
	}
	var cancellation directCommandAcceptance
	p107Decode(t, cancelled, &cancellation)
	if cancellation.CommandID != string(commandID) || cancellation.SessionID != session.SessionID || cancellation.KnownState.CommandState != string(domain.CommandStateCancelled) {
		t.Fatalf("cancel acceptance = %#v", cancellation)
	}
	replayedCancel := p107Do(handler, controller, true, http.MethodPost, cancelPath, nil, "p109-cancel-key")
	if replayedCancel.Code != http.StatusAccepted {
		t.Fatalf("cancel replay status = %d, body = %s", replayedCancel.Code, replayedCancel.Body.String())
	}
	var cancelReplay directCommandAcceptance
	p107Decode(t, replayedCancel, &cancelReplay)
	if cancelReplay.CommandID != string(commandID) || cancelReplay.KnownState.CommandState != string(domain.CommandStateCancelled) {
		t.Fatalf("cancel replay acceptance = %#v", cancelReplay)
	}

	wrongController := p107DirectController(t, "other-controller")
	deniedCancel := p107Do(handler, wrongController, true, http.MethodPost, cancelPath, nil, "p109-wrong-cancel-key")
	if deniedCancel.Code != http.StatusForbidden {
		t.Fatalf("cross-controller cancel status = %d, body = %s", deniedCancel.Code, deniedCancel.Body.String())
	}
	missingCancel := p107Do(handler, controller, true, http.MethodPost, "/v1/commands/cmd-p109-missing/cancel", nil, "p109-missing-cancel-key")
	if missingCancel.Code != http.StatusNotFound {
		t.Fatalf("missing command cancel status = %d, body = %s", missingCancel.Code, missingCancel.Body.String())
	}

	closePath := "/v1/sessions/" + session.SessionID
	closeBody := []byte(`{"policy":"graceful"}`)
	closed := p107Do(handler, controller, true, http.MethodDelete, closePath, closeBody, "p109-close-key")
	if closed.Code != http.StatusAccepted {
		t.Fatalf("close status = %d, body = %s", closed.Code, closed.Body.String())
	}
	var closeAcceptance directSessionAcceptance
	p107Decode(t, closed, &closeAcceptance)
	if closeAcceptance.SessionID != session.SessionID || closeAcceptance.KnownState.SessionState != string(domain.SessionStateClosed) {
		t.Fatalf("close acceptance = %#v", closeAcceptance)
	}
	replayedClose := p107Do(handler, controller, true, http.MethodDelete, closePath, closeBody, "p109-close-key")
	if replayedClose.Code != http.StatusAccepted {
		t.Fatalf("close replay status = %d, body = %s", replayedClose.Code, replayedClose.Body.String())
	}
	var closeReplay directSessionAcceptance
	p107Decode(t, replayedClose, &closeReplay)
	if closeReplay.SessionID != session.SessionID || closeReplay.KnownState.SessionState != string(domain.SessionStateClosed) {
		t.Fatalf("close replay acceptance = %#v", closeReplay)
	}
	changedPolicy := p107Do(handler, controller, true, http.MethodDelete, closePath, []byte(`{"policy":"drain"}`), "p109-close-key")
	if changedPolicy.Code != http.StatusConflict {
		t.Fatalf("changed close policy status = %d, body = %s", changedPolicy.Code, changedPolicy.Body.String())
	}
	var conflict directAPIError
	p107Decode(t, changedPolicy, &conflict)
	if conflict.Code != "idempotency_conflict" {
		t.Fatalf("changed close policy error = %#v", conflict)
	}

	readClosed := p107Do(handler, controller, true, http.MethodGet, closePath, nil, "")
	if readClosed.Code != http.StatusOK {
		t.Fatalf("closed session read status = %d, body = %s", readClosed.Code, readClosed.Body.String())
	}
	var snapshot directSessionReadResponse
	p107Decode(t, readClosed, &snapshot)
	if snapshot.Resource.SessionState != string(domain.SessionStateClosed) {
		t.Fatalf("session state after close = %q", snapshot.Resource.SessionState)
	}
	stored, err := authority.GetCommand(context.Background(), commandID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != domain.CommandStateCancelled || runtime.cancelCalls.Load() != 0 {
		t.Fatalf("queued cancel state=%s runtime cancel calls=%d", stored.State, runtime.cancelCalls.Load())
	}
}

func TestP109DirectCloseRejectsInvalidPolicyAndDefaultsWhenOmitted(t *testing.T) {
	runtime := &p109Runtime{generation: "p109-policy-generation"}
	service, _ := newP046Service(t, runtime)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")
	created := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "p109-policy-create-key")
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var session directSessionAcceptance
	p107Decode(t, created, &session)
	closePath := "/v1/sessions/" + session.SessionID

	for _, body := range []string{`{"policy":null}`, `{"policy":17}`, `{"policy":"   "}`, `{"policy":"graceful","controller":{}}`, `{"policy":"graceful"} {}`} {
		response := p107Do(handler, controller, true, http.MethodDelete, closePath, []byte(body), "p109-invalid-policy-"+strings.ReplaceAll(body, " ", ""))
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid close body %s status = %d, body = %s", body, response.Code, response.Body.String())
		}
	}

	defaulted := p107Do(handler, controller, true, http.MethodDelete, closePath, nil, "p109-default-policy-key")
	if defaulted.Code != http.StatusAccepted {
		t.Fatalf("default-policy close status = %d, body = %s", defaulted.Code, defaulted.Body.String())
	}
	var accepted directSessionAcceptance
	p107Decode(t, defaulted, &accepted)
	if accepted.KnownState.SessionState != string(domain.SessionStateClosed) {
		t.Fatalf("default-policy close acceptance = %#v", accepted)
	}
}

func TestP109UnconfirmedStopsReturnKnownLostState(t *testing.T) {
	runtime := &p109Runtime{generation: "p109-uncertain-stop-generation", uncertainCancel: true, uncertainStop: true}
	service, authority := newP046Service(t, runtime)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")

	created := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "p109-uncertain-cancel-create-key")
	if created.Code != http.StatusAccepted {
		t.Fatalf("cancel test session create status = %d, body = %s", created.Code, created.Body.String())
	}
	var cancelSession directSessionAcceptance
	p107Decode(t, created, &cancelSession)
	cancelCommandID := p109AcceptQueuedCommand(t, service, domain.SessionID(cancelSession.SessionID), controller, "cmd-p109-uncertain-cancel")
	started, err := authority.StartNextEligibleCommand(context.Background(), store.DefaultRunningCommandLimit)
	if err != nil {
		t.Fatal(err)
	}
	if started.CommandID != cancelCommandID || started.State != domain.CommandStateRunning {
		t.Fatalf("seed running command = %#v", started)
	}
	cancelled := p107Do(handler, controller, true, http.MethodPost, "/v1/commands/"+string(cancelCommandID)+"/cancel", nil, "p109-uncertain-cancel-key")
	if cancelled.Code != http.StatusAccepted {
		t.Fatalf("unconfirmed cancel status = %d, body = %s", cancelled.Code, cancelled.Body.String())
	}
	var cancelAcceptance directCommandAcceptance
	p107Decode(t, cancelled, &cancelAcceptance)
	if cancelAcceptance.KnownState.CommandState != string(domain.CommandStateLost) {
		t.Fatalf("unconfirmed cancel acceptance = %#v", cancelAcceptance)
	}
	storedCommand, err := authority.GetCommand(context.Background(), cancelCommandID)
	if err != nil {
		t.Fatal(err)
	}
	if storedCommand.State != domain.CommandStateLost || runtime.cancelCalls.Load() != 1 {
		t.Fatalf("unconfirmed cancel persisted state=%s, cancel calls=%d", storedCommand.State, runtime.cancelCalls.Load())
	}

	created = p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "p109-uncertain-close-create-key")
	if created.Code != http.StatusAccepted {
		t.Fatalf("close test session create status = %d, body = %s", created.Code, created.Body.String())
	}
	var closeSession directSessionAcceptance
	p107Decode(t, created, &closeSession)
	closed := p107Do(handler, controller, true, http.MethodDelete, "/v1/sessions/"+closeSession.SessionID, nil, "p109-uncertain-close-key")
	if closed.Code != http.StatusAccepted {
		t.Fatalf("unconfirmed close status = %d, body = %s", closed.Code, closed.Body.String())
	}
	var closeAcceptance directSessionAcceptance
	p107Decode(t, closed, &closeAcceptance)
	if closeAcceptance.KnownState.SessionState != string(domain.SessionStateLost) {
		t.Fatalf("unconfirmed close acceptance = %#v", closeAcceptance)
	}
}

func TestP109DirectCommandFailureIsOutcomeNotTransportFailure(t *testing.T) {
	runtime := &p109Runtime{generation: "p109-shell-failure-generation", exitCode: 23}
	service, authority := newP046Service(t, runtime)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")
	created := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "p109-shell-create-key")
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var session directSessionAcceptance
	p107Decode(t, created, &session)

	submitted := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions/"+session.SessionID+"/commands", []byte(`{"script":"exit 23"}`), "p109-shell-command-key")
	if submitted.Code != http.StatusAccepted {
		t.Fatalf("nonzero shell command submit status = %d, body = %s", submitted.Code, submitted.Body.String())
	}
	var acceptance directCommandAcceptance
	p107Decode(t, submitted, &acceptance)
	commandID := domain.CommandID(acceptance.CommandID)
	completed := p108WaitForCommandState(t, authority, commandID, domain.CommandStateFailed)
	if completed.ExitCode == nil || *completed.ExitCode != 23 {
		t.Fatalf("stored failed command exit code = %v, want 23", completed.ExitCode)
	}

	read := p107Do(handler, controller, true, http.MethodGet, "/v1/commands/"+string(commandID), nil, "")
	if read.Code != http.StatusOK {
		t.Fatalf("failed command read HTTP status = %d, body = %s", read.Code, read.Body.String())
	}
	var snapshot directCommandReadResponse
	p107Decode(t, read, &snapshot)
	if snapshot.Resource.CommandState != string(domain.CommandStateFailed) || snapshot.Resource.ExitCode == nil || *snapshot.Resource.ExitCode != 23 {
		t.Fatalf("failed command API snapshot = %#v", snapshot.Resource)
	}
}

func TestP109DirectLifecycleRoutesUseMappedLoopbackMTLS(t *testing.T) {
	runtime := &p109Runtime{generation: "p109-loopback-generation"}
	service, _ := newP046Service(t, runtime)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	pki := newP106TestPKI(t)
	options := writeP106TestFiles(t, t.TempDir(), pki, validP106PrincipalMap)
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
			t.Errorf("close loopback HTTPS server: %v", err)
		}
		if err := <-serveResult; err != nil {
			t.Errorf("serve loopback HTTPS server: %v", err)
		}
	})
	client := p106HTTPClient(pki.serverRoots, &pki.clientTLS, tls.VersionTLS13)

	createRequest, err := http.NewRequest(http.MethodPost, "https://"+server.Addr()+"/v1/sessions", strings.NewReader(p107CreateBody))
	if err != nil {
		t.Fatal(err)
	}
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Idempotency-Key", "p109-mtls-create-key")
	created, err := client.Do(createRequest)
	if err != nil {
		t.Fatalf("mTLS session create: %v", err)
	}
	defer created.Body.Close()
	if created.StatusCode != http.StatusAccepted {
		t.Fatalf("mTLS create status = %d, body = %s", created.StatusCode, p107HTTPBody(t, created))
	}
	var session directSessionAcceptance
	if err := json.NewDecoder(created.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	commandID := p109AcceptQueuedCommand(t, service, domain.SessionID(session.SessionID), p107DirectController(t, "tomasz.walczuk"), "cmd-p109-mtls-cancel")

	cancelRequest, err := http.NewRequest(http.MethodPost, "https://"+server.Addr()+"/v1/commands/"+string(commandID)+"/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelRequest.Header.Set("Idempotency-Key", "p109-mtls-cancel-key")
	cancelled, err := client.Do(cancelRequest)
	if err != nil {
		t.Fatalf("mTLS command cancel: %v", err)
	}
	defer cancelled.Body.Close()
	if cancelled.StatusCode != http.StatusAccepted {
		t.Fatalf("mTLS cancel status = %d, body = %s", cancelled.StatusCode, p107HTTPBody(t, cancelled))
	}
	var cancelAcceptance directCommandAcceptance
	if err := json.NewDecoder(cancelled.Body).Decode(&cancelAcceptance); err != nil {
		t.Fatal(err)
	}
	if cancelAcceptance.KnownState.CommandState != string(domain.CommandStateCancelled) {
		t.Fatalf("mTLS cancel acceptance = %#v", cancelAcceptance)
	}

	closeRequest, err := http.NewRequest(http.MethodDelete, "https://"+server.Addr()+"/v1/sessions/"+session.SessionID, strings.NewReader(`{"policy":"graceful"}`))
	if err != nil {
		t.Fatal(err)
	}
	closeRequest.Header.Set("Content-Type", "application/json")
	closeRequest.Header.Set("Idempotency-Key", "p109-mtls-close-key")
	closed, err := client.Do(closeRequest)
	if err != nil {
		t.Fatalf("mTLS session close: %v", err)
	}
	defer closed.Body.Close()
	if closed.StatusCode != http.StatusAccepted {
		t.Fatalf("mTLS close status = %d, body = %s", closed.StatusCode, p107HTTPBody(t, closed))
	}
	var closeAcceptance directSessionAcceptance
	if err := json.NewDecoder(closed.Body).Decode(&closeAcceptance); err != nil {
		t.Fatal(err)
	}
	if closeAcceptance.KnownState.SessionState != string(domain.SessionStateClosed) {
		t.Fatalf("mTLS close acceptance = %#v", closeAcceptance)
	}
}

func p109AcceptQueuedCommand(t *testing.T, service *execution.Service, sessionID domain.SessionID, controller domain.ControllerIdentity, commandIDText string) domain.CommandID {
	t.Helper()
	session, err := service.GetSession(context.Background(), sessionID, controller)
	if err != nil {
		t.Fatal(err)
	}
	commandID, err := domain.NewCommandID(commandIDText)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", []byte(`{"script":"queued before cancel"}`), domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := service.AcceptCommand(context.Background(), execution.SubmitCommandRequest{
		CommandID: commandID, SessionID: sessionID, Controller: controller, IdempotencyKey: commandIDText + "-submit",
		RequestHash: hash, Script: "queued before cancel", Timeout: session.Limits.CommandTimeout,
		IdempotencyRetention: store.DefaultSessionIdempotencyRetention,
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Command.State != domain.CommandStateQueued {
		t.Fatalf("seed command state = %s, want queued", accepted.Command.State)
	}
	return commandID
}

type p109Runtime struct {
	generation      string
	exitCode        int
	commandCalls    atomic.Int32
	cancelCalls     atomic.Int32
	stopCalls       atomic.Int32
	uncertainCancel bool
	uncertainStop   bool
}

func (r *p109Runtime) Prepare(context.Context, execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	return execution.RuntimePrepared{RuntimeGeneration: r.generation}, nil
}

func (r *p109Runtime) StartAgent(context.Context, execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	return execution.RuntimeStarted{RuntimeGeneration: r.generation}, nil
}

func (*p109Runtime) Cleanup(context.Context, execution.RuntimeCleanupRequest) error { return nil }

func (r *p109Runtime) ExecuteCommand(context.Context, execution.RuntimeCommandRequest) (execution.RuntimeCommandResult, error) {
	r.commandCalls.Add(1)
	return execution.RuntimeCommandResult{Stdout: []byte("p109-output\n"), ExitCode: r.exitCode}, nil
}

func (r *p109Runtime) CancelCommand(context.Context, execution.RuntimeCommandRequest) (execution.RuntimeCommandStopResult, error) {
	r.cancelCalls.Add(1)
	return execution.RuntimeCommandStopResult{Confirmed: !r.uncertainCancel}, nil
}

func (r *p109Runtime) StopSession(context.Context, store.SessionRecord) (bool, error) {
	r.stopCalls.Add(1)
	return !r.uncertainStop, nil
}

var _ execution.SessionRuntime = (*p109Runtime)(nil)
var _ execution.CommandRuntime = (*p109Runtime)(nil)
var _ execution.RuntimeCommandControl = (*p109Runtime)(nil)
