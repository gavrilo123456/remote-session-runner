package runnerd

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/localapi"
	"remote-session-runner/src/internal/store"
)

func TestP116I01UnixAndHTTPSCommandEventContractParity(t *testing.T) {
	const stdout = "p116 shared stdout\n"
	const stderr = "p116 shared stderr\n"
	runtime := &p116Runtime{generation: "p116-parity-generation", stdout: []byte(stdout), stderr: []byte(stderr)}
	service, authority, _ := p112NewService(t, runtime)
	directEndpoint, directClient, otherDirectClient := p112StartMappedServer(t, service)
	localOwner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	localClient := p116StartLocalUnixAPI(t, authority, localOwner)

	localSessionID := p116CreateLocalCommand(t, localClient, authority, localOwner, stdout, stderr)
	localCommandID, err := authority.GetLocalIntentByResource(context.Background(), "submit_command", localSessionID.commandID, localOwner)
	if err != nil {
		t.Fatal(err)
	}
	if localCommandID.CommandID == "" || string(localCommandID.SessionID) != localSessionID.sessionID {
		t.Fatalf("local command intent identity = %+v", localCommandID)
	}
	localResponse, err := localClient.Get("http://local/v1/commands/" + string(localCommandID.CommandID) + "/events?after=0")
	if err != nil {
		t.Fatal(err)
	}
	localEvents := p116ReadEventStream(t, localResponse, http.StatusOK, "authority", string(localCommandID.CommandID), "Unix")

	directController := p107DirectController(t, "tomasz.walczuk")
	status, body := p112Do(t, directClient, http.MethodPost, directEndpoint+"/v1/sessions", []byte(p107CreateBody), "p116-direct-create")
	if status != http.StatusAccepted {
		t.Fatalf("direct create status=%d body=%s", status, body)
	}
	var directSession directSessionAcceptance
	if err := json.Unmarshal(body, &directSession); err != nil {
		t.Fatalf("decode direct create body=%s: %v", body, err)
	}
	status, body = p112Do(t, directClient, http.MethodPost,
		directEndpoint+"/v1/sessions/"+directSession.SessionID+"/commands", []byte(`{"script":"printf p116"}`), "p116-direct-submit")
	if status != http.StatusAccepted {
		t.Fatalf("direct submit status=%d body=%s", status, body)
	}
	var directCommand directCommandAcceptance
	if err := json.Unmarshal(body, &directCommand); err != nil {
		t.Fatalf("decode direct submit body=%s: %v", body, err)
	}
	p116WaitForDirectCommand(t, service, directController, domain.CommandID(directCommand.CommandID))
	directResponse, err := directClient.Get(directEndpoint + "/v1/commands/" + directCommand.CommandID + "/events?after=0")
	if err != nil {
		t.Fatalf("direct event request: %v", err)
	}
	directEvents := p116ReadEventStream(t, directResponse, http.StatusOK, "authority", directCommand.CommandID, "HTTPS")
	if directResponse.Header.Get("X-Runner-View") != "authority" || directResponse.Header.Get("X-Runner-Stale") != "false" ||
		directResponse.Header.Get("X-Runner-Last-Sequence") != "5" {
		t.Fatalf("direct event headers=%v", directResponse.Header)
	}
	if len(directEvents) != 5 {
		t.Fatalf("direct event count=%d events=%+v, want five", len(directEvents), directEvents)
	}
	deniedStatus, deniedBody := p112Do(t, otherDirectClient, http.MethodGet, directEndpoint+"/v1/commands/"+directCommand.CommandID, nil, "")
	p112RequireError(t, deniedStatus, deniedBody, http.StatusForbidden, "controller_mismatch")

	wantTypes := []string{"command_queued", "command_started", "stdout", "stderr", "command_succeeded"}
	if got := p116EventTypes(localEvents); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("Unix event types=%v, want %v", got, wantTypes)
	}
	if got := p116EventTypes(directEvents); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("HTTPS event types=%v, want %v", got, wantTypes)
	}
	if len(localEvents) != len(directEvents) {
		t.Fatalf("Unix/HTTPS event counts differ: Unix=%d HTTPS=%d", len(localEvents), len(directEvents))
	}
	for index := range wantTypes {
		localEvent := localEvents[index]
		directEvent := directEvents[index]
		if localEvent.Sequence != int64(index+1) || directEvent.Sequence != int64(index+1) {
			t.Errorf("event %d sequence: Unix=%d HTTPS=%d", index, localEvent.Sequence, directEvent.Sequence)
		}
		if localEvent.CommandID != string(localCommandID.CommandID) || directEvent.CommandID != directCommand.CommandID {
			t.Errorf("event %d command identity: Unix=%q HTTPS=%q", index, localEvent.CommandID, directEvent.CommandID)
		}
		if localEvent.Timestamp.IsZero() || directEvent.Timestamp.IsZero() || localEvent.Timestamp.Location() != time.UTC || directEvent.Timestamp.Location() != time.UTC {
			t.Errorf("event %d timestamps must decode as UTC: Unix=%s HTTPS=%s", index, localEvent.Timestamp, directEvent.Timestamp)
		}
		if localEvent.Ordinal != directEvent.Ordinal {
			t.Errorf("event %d ordinal differs: Unix=%d HTTPS=%d", index, localEvent.Ordinal, directEvent.Ordinal)
		}
		if wantTypes[index] == "stdout" || wantTypes[index] == "stderr" {
			if localEvent.Encoding != "base64" || directEvent.Encoding != "base64" ||
				localEvent.DataBase64 != directEvent.DataBase64 || localEvent.ByteCount != directEvent.ByteCount {
				t.Errorf("event %d output projection differs: Unix=%+v HTTPS=%+v", index, localEvent, directEvent)
			}
		} else if localEvent.Encoding != "" || directEvent.Encoding != "" || localEvent.DataBase64 != "" || directEvent.DataBase64 != "" || localEvent.ByteCount != 0 || directEvent.ByteCount != 0 {
			t.Errorf("lifecycle event %d unexpectedly carries output fields: Unix=%+v HTTPS=%+v", index, localEvent, directEvent)
		}
	}
	if localEvents[0].Ordinal == 0 || localEvents[0].Ordinal != directEvents[0].Ordinal {
		t.Fatalf("queued-event ordinals must agree and be nonzero: Unix=%d HTTPS=%d", localEvents[0].Ordinal, directEvents[0].Ordinal)
	}

	localRead, err := localClient.Get("http://local/v1/commands/" + string(localCommandID.CommandID))
	if err != nil {
		t.Fatal(err)
	}
	defer localRead.Body.Close()
	if localRead.StatusCode != http.StatusOK {
		t.Fatalf("Unix command read status=%d", localRead.StatusCode)
	}
	var localCommandRead struct {
		View     string `json:"view"`
		Resource struct {
			CommandID string `json:"command_id"`
		} `json:"resource"`
	}
	if err := json.NewDecoder(localRead.Body).Decode(&localCommandRead); err != nil {
		t.Fatal(err)
	}
	if localCommandRead.View != "local_intent" || localCommandRead.Resource.CommandID != string(localCommandID.CommandID) {
		t.Fatalf("Unix command read identity/view=%+v", localCommandRead)
	}
	directReadStatus, directReadBody := p112Do(t, directClient, http.MethodGet, directEndpoint+"/v1/commands/"+directCommand.CommandID, nil, "")
	var directCommandRead directCommandReadResponse
	if directReadStatus != http.StatusOK {
		t.Fatalf("HTTPS command read status=%d body=%s", directReadStatus, directReadBody)
	}
	if err := json.Unmarshal(directReadBody, &directCommandRead); err != nil {
		t.Fatalf("decode HTTPS command read body=%s: %v", directReadBody, err)
	}
	if directCommandRead.View != "authority" || directCommandRead.Resource.CommandID != directCommand.CommandID {
		t.Fatalf("HTTPS command read identity/view=%+v", directCommandRead)
	}
}

type p116LocalResourceIDs struct {
	sessionID string
	commandID string
}

func p116StartLocalUnixAPI(t *testing.T, authority *store.AuthorityStore, owner domain.ControllerIdentity) *http.Client {
	t.Helper()
	socketDir, err := os.MkdirTemp("/tmp", "rsr-p116-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := socketDir + "/local-api.sock"
	server, err := localapi.NewServer(localapi.ServerOptions{Authority: authority, Owner: owner, SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()
	t.Cleanup(func() {
		if err := server.Close(context.Background()); err != nil {
			t.Errorf("close P116 Unix API: %v", err)
		}
		if err := <-serveErr; err != nil {
			t.Errorf("serve P116 Unix API: %v", err)
		}
	})
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socketPath)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

func p116CreateLocalCommand(t *testing.T, client *http.Client, authority *store.AuthorityStore, owner domain.ControllerIdentity, stdout, stderr string) p116LocalResourceIDs {
	t.Helper()
	sessionBody := []byte(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"source":{"mode":"empty"}}`)
	status, body := p116UnixRequest(t, client, http.MethodPost, "/v1/sessions", sessionBody, "p116-local-create")
	if status != http.StatusAccepted {
		t.Fatalf("Unix create status=%d body=%s", status, body)
	}
	var session struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(body, &session); err != nil || session.SessionID == "" {
		t.Fatalf("decode Unix session acceptance body=%s err=%v", body, err)
	}
	script := `printf p116`
	submitBody, err := json.Marshal(struct {
		Script string `json:"script"`
	}{Script: script})
	if err != nil {
		t.Fatal(err)
	}
	status, body = p116UnixRequest(t, client, http.MethodPost, "/v1/sessions/"+session.SessionID+"/commands", submitBody, "p116-local-submit")
	if status != http.StatusAccepted {
		t.Fatalf("Unix submit status=%d body=%s", status, body)
	}
	var accepted struct {
		CommandID string `json:"command_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(body, &accepted); err != nil || accepted.CommandID == "" || accepted.SessionID != session.SessionID {
		t.Fatalf("decode Unix command acceptance body=%s err=%v", body, err)
	}
	intent, err := authority.GetLocalIntentByResource(context.Background(), "submit_command", accepted.CommandID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if string(intent.ScriptBytes) != script {
		t.Fatalf("stored local script = %q, want %q", intent.ScriptBytes, script)
	}
	limits := domain.DefaultServiceLimits()
	effectiveLimits := domain.EffectiveSessionLimits{
		CommandTimeout: limits.CommandTimeout, IdleTimeout: limits.IdleTimeout,
		SessionMaxLifetime: limits.SessionMaxLifetime, OutputBytesPerCommand: limits.OutputBytesPerCommand,
	}
	if _, err := authority.CreateSession(context.Background(), store.SessionCreate{
		SessionID: intent.SessionID, Target: intent.Target, Environment: intent.Environment, Controller: intent.Controller,
		Source: intent.Source, Limits: effectiveLimits,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(context.Background(), intent.SessionID, domain.SessionStateReady, "P116 local authority ready"); err != nil {
		t.Fatal(err)
	}
	var ordinal int64
	if intent.IntentOrdinal != nil {
		ordinal = *intent.IntentOrdinal
	}
	command, _, err := authority.AcceptCommand(context.Background(), store.CommandAcceptance{
		CommandID: intent.CommandID, SessionID: intent.SessionID, RequestHash: intent.RequestHash,
		IdempotencyKey: "p116-local-authority", Script: script, Timeout: effectiveLimits.CommandTimeout,
		IntentOrdinal: ordinal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionCommand(context.Background(), store.CommandTransition{CommandID: command.CommandID, NextState: domain.CommandStateRunning}); err != nil {
		t.Fatal(err)
	}
	for _, output := range []struct {
		typeName string
		payload  string
	}{{typeName: "stdout", payload: stdout}, {typeName: "stderr", payload: stderr}} {
		if _, err := authority.AppendCommandEvent(context.Background(), store.CommandEventAppend{
			CommandID: command.CommandID, Type: output.typeName, Payload: []byte(output.payload), ByteCount: int64(len(output.payload)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := authority.CompleteRunningCommand(context.Background(), store.CommandTransition{
		CommandID: command.CommandID, NextState: domain.CommandStateSucceeded, ExitCode: p116Int(0), OutputComplete: true,
	}, domain.SessionStateReady, "P116 local authority completed", false); err != nil {
		t.Fatal(err)
	}
	return p116LocalResourceIDs{sessionID: session.SessionID, commandID: accepted.CommandID}
}

func p116UnixRequest(t *testing.T, client *http.Client, method, path string, body []byte, key string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, "http://local"+path, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if body == nil {
		request.Body = nil
	} else {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}

type p116WireEvent struct {
	CommandID  string    `json:"command_id"`
	Sequence   int64     `json:"sequence"`
	Type       string    `json:"type"`
	Timestamp  time.Time `json:"timestamp"`
	Ordinal    int64     `json:"ordinal,omitempty"`
	Encoding   string    `json:"encoding,omitempty"`
	DataBase64 string    `json:"data_base64,omitempty"`
	ByteCount  int64     `json:"byte_count,omitempty"`
}

func p116ReadEventStream(t *testing.T, response *http.Response, wantStatus int, wantView, wantCommandID, ingress string) []p116WireEvent {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("%s event response status=%d body=%s, want %d", ingress, response.StatusCode, body, wantStatus)
	}
	if response.Header.Get("Content-Type") != "application/x-ndjson" || response.Header.Get("X-Runner-View") != wantView || response.Header.Get("X-Runner-Stale") != "false" || response.Header.Get("X-Runner-Last-Sequence") != "5" {
		t.Fatalf("%s event response headers=%v", ingress, response.Header)
	}
	events := p116DecodeEventStream(t, response.Body)
	for index, event := range events {
		if event.CommandID != wantCommandID || event.Sequence != int64(index+1) || event.Timestamp.IsZero() {
			t.Errorf("%s event %d identity/sequence/timestamp=%+v", ingress, index, event)
		}
	}
	return events
}

func p116DecodeEventStream(t *testing.T, reader io.Reader) []p116WireEvent {
	t.Helper()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 64*1024)
	var events []p116WireEvent
	for scanner.Scan() {
		var event p116WireEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode NDJSON event %q: %v", scanner.Bytes(), err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func p116EventTypes(events []p116WireEvent) []string {
	result := make([]string, len(events))
	for index, event := range events {
		result[index] = event.Type
	}
	return result
}

func p116WaitForDirectCommand(t *testing.T, service *execution.Service, controller domain.ControllerIdentity, commandID domain.CommandID) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		command, err := service.GetCommand(context.Background(), commandID, controller)
		if err != nil {
			t.Fatal(err)
		}
		if command.State == domain.CommandStateSucceeded {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("direct command did not finish: state=%s", command.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func p116Int(value int) *int { return &value }

type p116Runtime struct {
	generation string
	stdout     []byte
	stderr     []byte
}

func (runtime *p116Runtime) Prepare(context.Context, execution.RuntimePrepareRequest) (execution.RuntimePrepared, error) {
	return execution.RuntimePrepared{RuntimeGeneration: runtime.generation}, nil
}

func (runtime *p116Runtime) StartAgent(context.Context, execution.RuntimeStartRequest) (execution.RuntimeStarted, error) {
	return execution.RuntimeStarted{RuntimeGeneration: runtime.generation}, nil
}

func (*p116Runtime) Cleanup(context.Context, execution.RuntimeCleanupRequest) error { return nil }

func (runtime *p116Runtime) ExecuteCommand(context.Context, execution.RuntimeCommandRequest) (execution.RuntimeCommandResult, error) {
	return execution.RuntimeCommandResult{Stdout: append([]byte(nil), runtime.stdout...), Stderr: append([]byte(nil), runtime.stderr...), ExitCode: 0}, nil
}

var _ execution.SessionRuntime = (*p116Runtime)(nil)
var _ execution.CommandRuntime = (*p116Runtime)(nil)
