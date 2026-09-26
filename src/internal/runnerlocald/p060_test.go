package runnerlocald

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

type p060FakeRuntime struct{}

func (*p060FakeRuntime) Prepare(context.Context, execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	return execution.RuntimePrepared{RuntimeGeneration: "p060-generation"}, nil
}
func (*p060FakeRuntime) StartAgent(context.Context, execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	return execution.RuntimeStarted{RuntimeGeneration: "p060-generation"}, nil
}
func (*p060FakeRuntime) Cleanup(context.Context, execution.RuntimeCleanupRequest) error { return nil }
func (*p060FakeRuntime) ExecuteCommand(context.Context, execution.RuntimeCommandRequest) (execution.RuntimeCommandResult, error) {
	return execution.RuntimeCommandResult{Stdout: []byte("p060-output\n"), ExitCode: 0}, nil
}

func TestP060AcceptIntentReloadsCommittedBytesAndRejectsForgedPayload(t *testing.T) {
	authority, service := newP060Service(t)
	socketPath := p060SocketPath(t)
	server, err := NewPrivateServer(PrivateServerOptions{Authority: authority, Service: service, SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	defer func() {
		if err := server.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := <-serveErr; err != nil {
			t.Fatal(err)
		}
	}()
	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %o, want 600", got)
	}
	intent := p060CreateIntent(t, authority, "intent-p060-create", "session-p060-create", "key-p060-create")
	client := p060UnixClient(socketPath)
	body := []byte(fmt.Sprintf(`{"intent_id":%q,"request_hash":%q}`, intent.IntentID, intent.RequestHash.String()))
	response := p060DoJSON(t, client, http.MethodPost, "http://locald/internal/v1/accept-intent", body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("accept status = %d body=%s", response.StatusCode, p060ReadBody(t, response))
	}
	var accepted intentAcceptanceResponse
	p060DecodeJSON(t, response, &accepted)
	if accepted.IntentID != string(intent.IntentID) || accepted.ResourceID != intent.ResourceID || accepted.SessionState != string(domain.SessionStateReady) || accepted.AcceptanceScope != "target_authority" {
		t.Fatalf("accept response = %+v", accepted)
	}
	if _, err := authority.GetSession(context.Background(), intent.SessionID); err != nil {
		t.Fatalf("accepted session = %v", err)
	}
	forged := []byte(fmt.Sprintf(`{"intent_id":%q,"request_hash":%q,"script":"rm -rf /"}`, intent.IntentID, intent.RequestHash.String()))
	forgedResponse := p060DoJSON(t, client, http.MethodPost, "http://locald/internal/v1/accept-intent", forged)
	if forgedResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged script status = %d body=%s", forgedResponse.StatusCode, p060ReadBody(t, forgedResponse))
	}
	wrongHash := []byte(fmt.Sprintf(`{"intent_id":%q,"request_hash":"v1:%064x"}`, intent.IntentID, 1))
	wrongResponse := p060DoJSON(t, client, http.MethodPost, "http://locald/internal/v1/accept-intent", wrongHash)
	if wrongResponse.StatusCode != http.StatusConflict {
		t.Fatalf("wrong hash status = %d body=%s", wrongResponse.StatusCode, p060ReadBody(t, wrongResponse))
	}
}

func TestP060AcceptIntentVerifiesOrdinalAndTargetBeforeService(t *testing.T) {
	authority, service := newP060Service(t)
	socketPath := p060SocketPath(t)
	server, err := NewPrivateServer(PrivateServerOptions{Authority: authority, Service: service, SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	defer func() { _ = server.Close(context.Background()); _ = <-serveErr }()
	create := p060CreateIntent(t, authority, "intent-p060-order-session", "session-p060-order", "key-p060-order-session")
	client := p060UnixClient(socketPath)
	createBody := []byte(fmt.Sprintf(`{"intent_id":%q,"request_hash":%q}`, create.IntentID, create.RequestHash.String()))
	if response := p060DoJSON(t, client, http.MethodPost, "http://locald/internal/v1/accept-intent", createBody); response.StatusCode != http.StatusAccepted {
		t.Fatalf("create status = %d body=%s", response.StatusCode, p060ReadBody(t, response))
	}
	submitInput := p060SubmitIntent(t, "intent-p060-order-command", string(create.SessionID), "command-p060-order", "key-p060-order-command", "echo exact")
	submit, err := authority.CreateLocalIntent(context.Background(), submitInput)
	if err != nil {
		t.Fatal(err)
	}
	wrongOrdinal := []byte(fmt.Sprintf(`{"intent_id":%q,"request_hash":%q,"intent_ordinal":99}`, submit.IntentID, submit.RequestHash.String()))
	wrong := p060DoJSON(t, client, http.MethodPost, "http://locald/internal/v1/accept-intent", wrongOrdinal)
	if wrong.StatusCode != http.StatusConflict {
		t.Fatalf("wrong ordinal status = %d body=%s", wrong.StatusCode, p060ReadBody(t, wrong))
	}
	correct := []byte(fmt.Sprintf(`{"intent_id":%q,"request_hash":%q,"intent_ordinal":%d}`, submit.IntentID, submit.RequestHash.String(), *submit.IntentOrdinal))
	accepted := p060DoJSON(t, client, http.MethodPost, "http://locald/internal/v1/accept-intent", correct)
	if accepted.StatusCode != http.StatusAccepted {
		t.Fatalf("correct ordinal status = %d body=%s", accepted.StatusCode, p060ReadBody(t, accepted))
	}
	remoteInput := p060RemoteSubmitIntent(t, "intent-p060-remote", "session-p060-remote", "command-p060-remote", "key-p060-remote", "echo remote")
	remote, err := authority.CreateLocalIntent(context.Background(), remoteInput)
	if err != nil {
		t.Fatal(err)
	}
	remoteResponse := p060DoJSON(t, client, http.MethodPost, "http://locald/internal/v1/accept-intent", []byte(fmt.Sprintf(`{"intent_id":%q,"request_hash":%q,"intent_ordinal":1}`, remote.IntentID, remote.RequestHash.String())))
	if remoteResponse.StatusCode != http.StatusConflict {
		t.Fatalf("remote target status = %d body=%s", remoteResponse.StatusCode, p060ReadBody(t, remoteResponse))
	}
}

func TestP060LocaldRestartReloadsCommittedIntent(t *testing.T) {
	authority, service := newP060Service(t)
	socketPath := p060SocketPath(t)
	intent := p060CreateIntent(t, authority, "intent-p060-restart", "session-p060-restart", "key-p060-restart")

	server, serveErr := p060StartServer(t, authority, service, socketPath)
	client := p060UnixClient(socketPath)
	body := []byte(fmt.Sprintf(`{"intent_id":%q,"request_hash":%q}`, intent.IntentID, intent.RequestHash.String()))
	first := p060DoJSON(t, client, http.MethodPost, "http://locald/internal/v1/accept-intent", body)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first accept status = %d body=%s", first.StatusCode, p060ReadBody(t, first))
	}
	if err := server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}

	server, serveErr = p060StartServer(t, authority, service, socketPath)
	defer func() {
		if err := server.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := <-serveErr; err != nil {
			t.Fatal(err)
		}
	}()
	second := p060DoJSON(t, client, http.MethodPost, "http://locald/internal/v1/accept-intent", body)
	if second.StatusCode != http.StatusAccepted {
		t.Fatalf("restarted accept status = %d body=%s", second.StatusCode, p060ReadBody(t, second))
	}
	var accepted intentAcceptanceResponse
	p060DecodeJSON(t, second, &accepted)
	if !accepted.Duplicate || accepted.IntentID != string(intent.IntentID) || accepted.SessionState != string(domain.SessionStateReady) {
		t.Fatalf("restarted accept response = %+v", accepted)
	}
}

func newP060Service(t *testing.T) (*store.AuthorityStore, *execution.Service) {
	t.Helper()
	root := testfixture.New(t)
	db, err := store.Open(context.Background(), root.Path()+"/state/p060.db")
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
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	environment, err := domain.NewEnvironment(domain.EnvironmentSpec{Name: "mac-dev", HostClass: "mac", EffectiveAccount: "tomasz.walczuk", AllowedTargets: []domain.ExecutionTarget{target}, AllowedSourceModes: []domain.SourceMode{domain.SourceModeEmpty}, AllowedControllers: []domain.ControllerIdentity{controller}, ServiceLimits: domain.DefaultServiceLimits()})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := execution.NewEnvironmentRegistry(environment)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &p060FakeRuntime{}
	service, err := execution.NewExecutionService(authority, runtime, registry, execution.RealClock{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return authority, service
}

func p060CreateIntent(t *testing.T, authority *store.AuthorityStore, intentID, sessionID, key string) store.LocalIntentRecord {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(fmt.Sprintf(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"operation":"create_session","session_id":%q}`, sessionID))
	canonical, err := domain.CanonicalizeMutationRequestJSON("create_session", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("create_session", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	input := store.LocalIntentCreate{IntentID: domain.IntentID(intentID), Operation: "create_session", ResourceID: sessionID, SessionID: domain.SessionID(sessionID), Target: target, Environment: "mac-dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: key, PayloadJSON: canonical}
	record, err := authority.CreateLocalIntent(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func p060RemoteSubmitIntent(t *testing.T, intentID, sessionID, commandID, key, script string) store.LocalIntentCreate {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(fmt.Sprintf(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"operation":"submit_command","script":%q,"session_id":%q}`, script, sessionID))
	canonical, err := domain.CanonicalizeMutationRequestJSON("submit_command", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ordinal := int64(1)
	return store.LocalIntentCreate{IntentID: domain.IntentID(intentID), Operation: "submit_command", ResourceID: commandID, SessionID: domain.SessionID(sessionID), CommandID: domain.CommandID(commandID), Target: target, Environment: "linux-dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: key, PayloadJSON: canonical, ScriptBytes: []byte(script), IntentOrdinal: &ordinal}
}

func p060SubmitIntent(t *testing.T, intentID, sessionID, commandID, key, script string) store.LocalIntentCreate {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(fmt.Sprintf(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"operation":"submit_command","script":%q,"session_id":%q}`, script, sessionID))
	canonical, err := domain.CanonicalizeMutationRequestJSON("submit_command", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store.LocalIntentCreate{IntentID: domain.IntentID(intentID), Operation: "submit_command", ResourceID: commandID, SessionID: domain.SessionID(sessionID), CommandID: domain.CommandID(commandID), Target: target, Environment: "mac-dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: key, PayloadJSON: canonical, ScriptBytes: []byte(script)}
}

func p060UnixClient(socketPath string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socketPath)
	}}}
}

func p060StartServer(t *testing.T, authority *store.AuthorityStore, service *execution.Service, socketPath string) (*PrivateServer, <-chan error) {
	t.Helper()
	server, err := NewPrivateServer(PrivateServerOptions{Authority: authority, Service: service, SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	return server, serveErr
}

func p060SocketPath(t *testing.T) string {
	t.Helper()
	path, err := os.MkdirTemp("/tmp", "r60-")
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return filepath.Join(path, "locald.sock")
}

func p060DoJSON(t *testing.T, client *http.Client, method, rawURL string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func p060ReadBody(t *testing.T, response *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func p060DecodeJSON(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}
