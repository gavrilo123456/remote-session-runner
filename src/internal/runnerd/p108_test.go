package runnerd

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
)

func TestP108DirectCommandSubmitReadReplayAndControllerDenial(t *testing.T) {
	runtime := &p108Runtime{
		generation:     "p108-command-generation",
		commandStarted: make(chan domain.CommandID, 4),
		commandDone:    make(chan domain.CommandID, 4),
		releaseCommand: make(chan struct{}, 4),
	}
	service, authority := newP046Service(t, runtime)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")
	created := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "p108-create-key")
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var session directSessionAcceptance
	p107Decode(t, created, &session)

	body, err := json.Marshal(map[string]any{"script": "printf p108"})
	if err != nil {
		t.Fatal(err)
	}
	submitted := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions/"+session.SessionID+"/commands", body, "p108-submit-key")
	if submitted.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %s", submitted.Code, submitted.Body.String())
	}
	var acceptance directCommandAcceptance
	p107Decode(t, submitted, &acceptance)
	if acceptance.ResourceID == "" || acceptance.ResourceID != acceptance.CommandID || acceptance.SessionID != session.SessionID ||
		acceptance.AcceptanceScope != "target_authority" || acceptance.ExecutionTarget.Kind != "remote" ||
		acceptance.ExecutionTarget.Profile != "linux-host" || acceptance.KnownState.CommandState != string(domain.CommandStateQueued) {
		t.Fatalf("command acceptance = %#v", acceptance)
	}

	select {
	case commandID := <-runtime.commandStarted:
		if commandID != domain.CommandID(acceptance.CommandID) {
			t.Fatalf("runtime started %q, want %q", commandID, acceptance.CommandID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("accepted command did not start")
	}

	read := p107Do(handler, controller, true, http.MethodGet, "/v1/commands/"+acceptance.CommandID, nil, "")
	if read.Code != http.StatusOK {
		t.Fatalf("command read status = %d, body = %s", read.Code, read.Body.String())
	}
	readBody := append([]byte(nil), read.Body.Bytes()...)
	var snapshot directCommandReadResponse
	p107Decode(t, read, &snapshot)
	resource := snapshot.Resource
	if snapshot.View != "authority" || snapshot.IsStale || resource.CommandID != acceptance.CommandID ||
		resource.SessionID != session.SessionID || resource.CommandState != string(domain.CommandStateRunning) ||
		resource.ExecutionTarget.Kind != "remote" || resource.Authority != "remote" ||
		resource.Controller.Type != string(domain.ControllerTypeDirectMTLS) || resource.Controller.ID != "tomasz.walczuk" ||
		resource.Environment != "linux-dev" || resource.Capabilities.EffectiveAccount != "ubuntu" || resource.Capabilities.Isolation != string(domain.IsolationOSUser) {
		t.Fatalf("active command snapshot = %#v", snapshot)
	}
	var rawRead map[string]any
	if err := json.Unmarshal(readBody, &rawRead); err != nil {
		t.Fatal(err)
	}
	readResource := rawRead["resource"].(map[string]any)
	if _, exists := readResource["script_sha256"]; exists {
		t.Fatal("direct command resource exposed private script hash metadata")
	}
	if _, exists := readResource["script_byte_count"]; exists {
		t.Fatal("direct command resource exposed private script byte metadata")
	}

	otherController := p107DirectController(t, "other-controller")
	deniedSubmit := p107Do(handler, otherController, true, http.MethodPost, "/v1/sessions/"+session.SessionID+"/commands", body, "p108-other-submit")
	if deniedSubmit.Code != http.StatusForbidden {
		t.Fatalf("wrong-controller submit status = %d, body = %s", deniedSubmit.Code, deniedSubmit.Body.String())
	}
	var submitDenial directAPIError
	p107Decode(t, deniedSubmit, &submitDenial)
	if submitDenial.Code != "controller_mismatch" {
		t.Fatalf("wrong-controller submit error = %#v", submitDenial)
	}
	deniedRead := p107Do(handler, otherController, true, http.MethodGet, "/v1/commands/"+acceptance.CommandID, nil, "")
	if deniedRead.Code != http.StatusForbidden {
		t.Fatalf("wrong-controller read status = %d, body = %s", deniedRead.Code, deniedRead.Body.String())
	}

	runtime.releaseCommand <- struct{}{}
	p108WaitForCommandState(t, authority, domain.CommandID(acceptance.CommandID), domain.CommandStateSucceeded)
	completed := p107Do(handler, controller, true, http.MethodGet, "/v1/commands/"+acceptance.CommandID, nil, "")
	if completed.Code != http.StatusOK {
		t.Fatalf("completed command read status = %d, body = %s", completed.Code, completed.Body.String())
	}
	var completedSnapshot directCommandReadResponse
	p107Decode(t, completed, &completedSnapshot)
	if completedSnapshot.Resource.ExitCode == nil || *completedSnapshot.Resource.ExitCode != 0 ||
		completedSnapshot.Resource.FinalEventSequence == nil || !completedSnapshot.Resource.OutputComplete {
		t.Fatalf("completed command snapshot = %#v", completedSnapshot)
	}

	replay := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions/"+session.SessionID+"/commands", body, "p108-submit-key")
	if replay.Code != http.StatusAccepted {
		t.Fatalf("same-key replay status = %d, body = %s", replay.Code, replay.Body.String())
	}
	var replayAcceptance directCommandAcceptance
	p107Decode(t, replay, &replayAcceptance)
	if replayAcceptance.CommandID != acceptance.CommandID {
		t.Fatalf("same-key replay command = %q, want %q", replayAcceptance.CommandID, acceptance.CommandID)
	}
	changedBody, err := json.Marshal(map[string]any{"script": "printf changed"})
	if err != nil {
		t.Fatal(err)
	}
	conflict := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions/"+session.SessionID+"/commands", changedBody, "p108-submit-key")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed-payload replay status = %d, body = %s", conflict.Code, conflict.Body.String())
	}
	var conflictError directAPIError
	p107Decode(t, conflict, &conflictError)
	if conflictError.Code != "idempotency_conflict" {
		t.Fatalf("changed-payload replay error = %#v", conflictError)
	}

	secondCreate := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "p108-second-create-key")
	if secondCreate.Code != http.StatusAccepted {
		t.Fatalf("second create status = %d, body = %s", secondCreate.Code, secondCreate.Body.String())
	}
	var secondSession directSessionAcceptance
	p107Decode(t, secondCreate, &secondSession)
	otherSessionReuse := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions/"+secondSession.SessionID+"/commands", body, "p108-submit-key")
	if otherSessionReuse.Code != http.StatusConflict {
		t.Fatalf("same key on another session status = %d, body = %s", otherSessionReuse.Code, otherSessionReuse.Body.String())
	}
	secondCommands, err := authority.ListSessionCommands(context.Background(), domain.SessionID(secondSession.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(secondCommands) != 0 {
		t.Fatalf("same key on another session created commands: %#v", secondCommands)
	}
	firstCommands, err := authority.ListSessionCommands(context.Background(), domain.SessionID(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(firstCommands) != 1 || runtime.commandCalls.Load() != 1 {
		t.Fatalf("commands=%d runtime calls=%d, want one each", len(firstCommands), runtime.commandCalls.Load())
	}
}

func TestP108ScriptUTF8ByteLimitLeavesNoOversizeCommandOrEvent(t *testing.T) {
	runtime := &p108Runtime{generation: "p108-limit-generation", commandDone: make(chan domain.CommandID, 4)}
	service, authority := newP046Service(t, runtime)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")
	created := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "p108-limit-create-key")
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var session directSessionAcceptance
	p107Decode(t, created, &session)

	// Two-byte code points prove the limit is measured in UTF-8 bytes, not runes.
	exactScript := strings.Repeat("é", domain.MaxScriptUTF8Bytes/2)
	exactBody, err := json.Marshal(map[string]string{"script": exactScript})
	if err != nil {
		t.Fatal(err)
	}
	accepted := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions/"+session.SessionID+"/commands", exactBody, "p108-exact-script-key")
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("exact-limit script status = %d, body = %s", accepted.Code, accepted.Body.String())
	}
	var command directCommandAcceptance
	p107Decode(t, accepted, &command)
	p108WaitForCommandState(t, authority, domain.CommandID(command.CommandID), domain.CommandStateSucceeded)
	stored, err := authority.GetCommand(context.Background(), domain.CommandID(command.CommandID))
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.ScriptBytes) != domain.MaxScriptUTF8Bytes {
		t.Fatalf("stored script bytes = %d, want %d", len(stored.ScriptBytes), domain.MaxScriptUTF8Bytes)
	}

	tooLarge := strings.Repeat("é", domain.MaxScriptUTF8Bytes/2+1)
	tooLargeBody, err := json.Marshal(map[string]string{"script": tooLarge})
	if err != nil {
		t.Fatal(err)
	}
	rejected := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions/"+session.SessionID+"/commands", tooLargeBody, "p108-oversize-script-key")
	if rejected.Code != http.StatusUnprocessableEntity {
		t.Fatalf("oversize script status = %d, body = %s", rejected.Code, rejected.Body.String())
	}
	var rejection directAPIError
	p107Decode(t, rejected, &rejection)
	if rejection.Code != "invalid_request" {
		t.Fatalf("oversize script error = %#v", rejection)
	}
	commands, err := authority.ListSessionCommands(context.Background(), domain.SessionID(session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || runtime.commandCalls.Load() != 1 {
		t.Fatalf("oversize script changed authority: commands=%d runtime calls=%d", len(commands), runtime.commandCalls.Load())
	}
	events, err := authority.ListCommandEvents(context.Background(), domain.CommandID(command.CommandID))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].Type != "command_queued" {
		t.Fatalf("accepted command events = %#v", events)
	}
	if runtime.commandCalls.Load() != 1 {
		t.Fatal("oversize script caused another runtime command")
	}
}

func TestP108DirectPreReadySubmitReturnsSessionNotReadyWithoutAcceptance(t *testing.T) {
	runtime := &p108Runtime{
		generation:      "p108-pre-ready-generation",
		preparedSession: make(chan domain.SessionID, 1),
		releaseReady:    make(chan struct{}, 1),
	}
	service, authority := newP046Service(t, runtime)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")
	createDone := make(chan *httptestResponse, 1)
	go func() {
		response := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "p108-pre-ready-create-key")
		createDone <- &httptestResponse{code: response.Code, body: append([]byte(nil), response.Body.Bytes()...)}
	}()
	defer func() {
		select {
		case runtime.releaseReady <- struct{}{}:
		default:
		}
	}()

	var sessionID domain.SessionID
	select {
	case sessionID = <-runtime.preparedSession:
	case <-time.After(3 * time.Second):
		t.Fatal("session creation did not reach runtime preparation")
	}
	creating, err := service.GetSession(context.Background(), sessionID, controller)
	if err != nil {
		t.Fatal(err)
	}
	if creating.State != domain.SessionStateCreating {
		t.Fatalf("session state before readiness = %s, want creating", creating.State)
	}

	script := []byte(`{"script":"echo too-early"}`)
	response := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions/"+string(sessionID)+"/commands", script, "p108-pre-ready-submit-key")
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("pre-ready submit status = %d, body = %s", response.Code, response.Body.String())
	}
	var rejection directAPIError
	p107Decode(t, response, &rejection)
	if rejection.Code != "session_not_ready" {
		t.Fatalf("pre-ready submit error = %#v", rejection)
	}
	commands, err := authority.ListSessionCommands(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 0 || runtime.commandCalls.Load() != 0 {
		t.Fatalf("pre-ready submit created work: commands=%d runtime calls=%d", len(commands), runtime.commandCalls.Load())
	}

	runtime.releaseReady <- struct{}{}
	select {
	case created := <-createDone:
		if created.code != http.StatusAccepted {
			t.Fatalf("create status after readiness = %d, body = %s", created.code, created.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session create handler did not return after readiness")
	}
}

func TestP108DirectCommandRoutesUseMappedLoopbackMTLS(t *testing.T) {
	runtime := &p108Runtime{generation: "p108-loopback-generation"}
	service, authority := newP046Service(t, runtime)
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
	createRequest, err := http.NewRequest(http.MethodPost, "https://"+server.Addr()+"/v1/sessions", bytes.NewReader([]byte(p107CreateBody)))
	if err != nil {
		t.Fatal(err)
	}
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("Idempotency-Key", "p108-loopback-create-key")
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

	commandRequest, err := http.NewRequest(http.MethodPost, "https://"+server.Addr()+"/v1/sessions/"+session.SessionID+"/commands", strings.NewReader(`{"script":"printf tls"}`))
	if err != nil {
		t.Fatal(err)
	}
	commandRequest.Header.Set("Content-Type", "application/json")
	commandRequest.Header.Set("Idempotency-Key", "p108-loopback-command-key")
	submitted, err := client.Do(commandRequest)
	if err != nil {
		t.Fatalf("mTLS command submit: %v", err)
	}
	defer submitted.Body.Close()
	if submitted.StatusCode != http.StatusAccepted {
		t.Fatalf("mTLS submit status = %d, body = %s", submitted.StatusCode, p107HTTPBody(t, submitted))
	}
	var command directCommandAcceptance
	if err := json.NewDecoder(submitted.Body).Decode(&command); err != nil {
		t.Fatal(err)
	}
	p108WaitForCommandState(t, authority, domain.CommandID(command.CommandID), domain.CommandStateSucceeded)

	readURL := "https://" + server.Addr() + "/v1/commands/" + command.CommandID
	read, err := client.Get(readURL)
	if err != nil {
		t.Fatalf("mTLS command read: %v", err)
	}
	defer read.Body.Close()
	if read.StatusCode != http.StatusOK {
		t.Fatalf("mTLS command read status = %d, body = %s", read.StatusCode, p107HTTPBody(t, read))
	}
	var snapshot directCommandReadResponse
	if err := json.NewDecoder(read.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.View != "authority" || snapshot.IsStale || snapshot.Resource.CommandID != command.CommandID ||
		snapshot.Resource.Controller.ID != "tomasz.walczuk" || snapshot.Resource.Authority != "remote" {
		t.Fatalf("mTLS command snapshot = %#v", snapshot)
	}
}

type p108Runtime struct {
	generation      string
	preparedSession chan domain.SessionID
	releaseReady    chan struct{}
	commandStarted  chan domain.CommandID
	commandDone     chan domain.CommandID
	releaseCommand  chan struct{}
	commandCalls    atomic.Int32
}

func (r *p108Runtime) Prepare(_ context.Context, request execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	if r.preparedSession != nil {
		r.preparedSession <- request.Session.SessionID
	}
	return execution.RuntimePrepared{RuntimeGeneration: r.generation}, nil
}

func (r *p108Runtime) StartAgent(ctx context.Context, _ execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	if r.releaseReady != nil {
		select {
		case <-r.releaseReady:
		case <-ctx.Done():
			return execution.RuntimeStarted{}, ctx.Err()
		}
	}
	return execution.RuntimeStarted{RuntimeGeneration: r.generation}, nil
}

func (*p108Runtime) Cleanup(context.Context, execution.RuntimeCleanupRequest) error { return nil }

func (r *p108Runtime) ExecuteCommand(ctx context.Context, request execution.RuntimeCommandRequest) (execution.RuntimeCommandResult, error) {
	r.commandCalls.Add(1)
	if r.commandStarted != nil {
		r.commandStarted <- request.Command.CommandID
	}
	if r.releaseCommand != nil {
		select {
		case <-r.releaseCommand:
		case <-ctx.Done():
			return execution.RuntimeCommandResult{}, ctx.Err()
		}
	}
	if r.commandDone != nil {
		r.commandDone <- request.Command.CommandID
	}
	return execution.RuntimeCommandResult{Stdout: []byte("p108-output\n"), ExitCode: 0}, nil
}

type httptestResponse struct {
	code int
	body []byte
}

func p108WaitForCommandState(t *testing.T, authority *store.AuthorityStore, commandID domain.CommandID, want domain.CommandState) store.CommandRecord {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		command, err := authority.GetCommand(context.Background(), commandID)
		if err == nil && command.State == want {
			return command
		}
		if err != nil && !errors.Is(err, store.ErrCommandNotFound) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	command, err := authority.GetCommand(context.Background(), commandID)
	if err != nil {
		t.Fatalf("command state lookup: %v", err)
	}
	t.Fatalf("command state = %s, want %s", command.State, want)
	return store.CommandRecord{}
}

func TestP108DirectRequestRejectsUnknownOrInvalidJSONWithoutAcceptance(t *testing.T) {
	runtime := &p108Runtime{generation: "p108-invalid-generation"}
	service, authority := newP046Service(t, runtime)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")
	created := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "p108-invalid-create-key")
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var session directSessionAcceptance
	p107Decode(t, created, &session)

	for _, test := range []struct {
		name string
		body []byte
		want int
	}{
		{name: "forged controller field", body: []byte(`{"script":"echo no","controller":{"controller_type":"direct_mtls","controller_id":"other"}}`), want: http.StatusBadRequest},
		{name: "target override", body: []byte(`{"script":"echo no","execution_target":{"kind":"local","profile":"mac-workstation"}}`), want: http.StatusBadRequest},
		{name: "missing script", body: []byte(`{"timeout_seconds":10}`), want: http.StatusUnprocessableEntity},
		{name: "invalid surrogate escape", body: []byte(`{"script":"\ud800"}`), want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions/"+session.SessionID+"/commands", test.body, "p108-invalid-"+strings.ReplaceAll(test.name, " ", "-"))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.want, response.Body.String())
			}
			commands, err := authority.ListSessionCommands(context.Background(), domain.SessionID(session.SessionID))
			if err != nil {
				t.Fatal(err)
			}
			if len(commands) != 0 || runtime.commandCalls.Load() != 0 {
				t.Fatalf("invalid request created work: commands=%d runtime calls=%d", len(commands), runtime.commandCalls.Load())
			}
		})
	}
}

func TestP108CommandRequestPathBindsIdempotencyToSession(t *testing.T) {
	runtime := &p108Runtime{generation: "p108-path-generation"}
	service, authority := newP046Service(t, runtime)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")
	var sessions []directSessionAcceptance
	for index, key := range []string{"p108-path-create-a", "p108-path-create-b"} {
		created := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), key)
		if created.Code != http.StatusAccepted {
			t.Fatalf("create %d status = %d, body = %s", index, created.Code, created.Body.String())
		}
		var session directSessionAcceptance
		p107Decode(t, created, &session)
		sessions = append(sessions, session)
	}
	body := []byte(`{"script":"echo bound"}`)
	first := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions/"+sessions[0].SessionID+"/commands", body, "p108-path-shared-key")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first submit status = %d, body = %s", first.Code, first.Body.String())
	}
	var firstAcceptance directCommandAcceptance
	p107Decode(t, first, &firstAcceptance)
	p108WaitForCommandState(t, authority, domain.CommandID(firstAcceptance.CommandID), domain.CommandStateSucceeded)
	second := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions/"+sessions[1].SessionID+"/commands", body, "p108-path-shared-key")
	if second.Code != http.StatusConflict {
		t.Fatalf("same key on a different route session status = %d, body = %s", second.Code, second.Body.String())
	}
	commands, err := authority.ListSessionCommands(context.Background(), domain.SessionID(sessions[1].SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 0 {
		t.Fatalf("same key on another session accepted command: %#v", commands)
	}
}

var _ execution.SessionRuntime = (*p108Runtime)(nil)
var _ execution.CommandRuntime = (*p108Runtime)(nil)
