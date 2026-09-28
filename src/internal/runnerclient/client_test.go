package runnerclient

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	p118SessionID = "same-opaque-session-id"
	p118CommandID = "same-opaque-command-id"
	p118JobID     = "same-opaque-job-id"
)

func TestP118ClientV1ContractBothTransports(t *testing.T) {
	testCases := []struct {
		name string
		new  func(t *testing.T, handler http.Handler) *Client
	}{
		{name: "unix socket", new: p118NewUnixClient},
		{name: "mutual TLS HTTPS", new: p118NewHTTPSClient},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := &p118Fixture{view: "local_intent"}
			if testCase.name == "mutual TLS HTTPS" {
				fixture.view = "authority"
			}
			client := testCase.new(t, fixture)
			defer client.CloseIdleConnections()
			p118ExerciseV1Contract(t, client, fixture)
		})
	}
}

func p118ExerciseV1Contract(t *testing.T, client *Client, fixture *p118Fixture) {
	t.Helper()
	ctx := context.Background()
	if client.EndpointKind() == EndpointUnixSocket && fixture.view != "local_intent" {
		t.Fatalf("Unix client fixture view=%q", fixture.view)
	}
	if client.EndpointKind() == EndpointHTTPS && fixture.view != "authority" {
		t.Fatalf("HTTPS client fixture view=%q", fixture.view)
	}

	acceptedSession, err := client.CreateSession(ctx, CreateSessionRequest{
		Environment: "linux-dev", ExecutionTarget: Target{Kind: "remote", Profile: "linux-host"},
		Source: json.RawMessage(`{"mode":"empty"}`),
	}, "p118-create-session")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if acceptedSession.SessionID != p118SessionID || acceptedSession.ResourceID != p118SessionID || acceptedSession.ExecutionTarget.Kind != "remote" {
		t.Fatalf("session acceptance=%+v", acceptedSession)
	}

	session, err := client.GetSession(ctx, p118SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.View != fixture.view || session.Resource.SessionID != p118SessionID || session.Resource.ExecutionTarget.Profile != "linux-host" {
		t.Fatalf("session snapshot=%+v", session)
	}
	if fixture.view == "local_intent" && session.Resource.DeliveryState != "recorded" {
		t.Fatalf("local intent delivery state=%q", session.Resource.DeliveryState)
	}
	if fixture.view == "authority" && (session.Resource.SessionState != "ready" || session.Resource.Authority != "linux") {
		t.Fatalf("authority session snapshot=%+v", session.Resource)
	}

	acceptedCommand, err := client.SubmitCommand(ctx, p118SessionID, SubmitCommandRequest{Script: "printf client-contract"}, "p118-submit-command")
	if err != nil {
		t.Fatalf("submit command: %v", err)
	}
	if acceptedCommand.CommandID != p118CommandID || acceptedCommand.SessionID != p118SessionID {
		t.Fatalf("command acceptance=%+v", acceptedCommand)
	}
	command, err := client.GetCommand(ctx, p118CommandID)
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if command.Resource.CommandID != p118CommandID || command.Resource.SessionID != p118SessionID {
		t.Fatalf("command snapshot=%+v", command)
	}
	if fixture.view == "local_intent" && command.Resource.DeliveryState != "recorded" {
		t.Fatalf("local command intent delivery state=%q", command.Resource.DeliveryState)
	}
	if fixture.view == "authority" && command.Resource.CommandState != "succeeded" {
		t.Fatalf("authority command state=%q", command.Resource.CommandState)
	}

	stream, err := client.StreamCommandEvents(ctx, p118CommandID, 2, true)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	first, err := stream.Next()
	if err != nil {
		t.Fatalf("read first event: %v", err)
	}
	if first.Sequence != 3 || first.Type != "stdout" || first.CommandID != p118CommandID || string(first.Data) != "\x00out" || first.ByteCount != 4 || first.Timestamp.Location() != time.UTC {
		t.Fatalf("first event=%+v decoded=%q", first, first.Data)
	}
	if stream.Cursor() != 3 {
		t.Fatalf("event cursor after first frame=%d, want 3", stream.Cursor())
	}
	second, err := stream.Next()
	if err != nil {
		t.Fatalf("read terminal event: %v", err)
	}
	if second.Sequence != 4 || second.Type != "command_succeeded" || stream.Cursor() != 4 {
		t.Fatalf("second event=%+v cursor=%d", second, stream.Cursor())
	}
	if _, err := stream.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("event end error=%v, want EOF", err)
	}
	_ = stream.Close()

	if _, err := client.CancelCommand(ctx, p118CommandID, "p118-cancel"); err != nil {
		t.Fatalf("cancel command: %v", err)
	}
	closed, err := client.CloseSession(ctx, p118SessionID, "", "p118-close")
	if err != nil {
		t.Fatalf("close session: %v", err)
	}
	if closed.SessionID != p118SessionID {
		t.Fatalf("close acceptance=%+v", closed)
	}

	acceptedJob, err := client.Run(ctx, RunJobRequest{
		Environment: "linux-dev", ExecutionTarget: Target{Kind: "remote", Profile: "linux-host"}, Script: "printf job",
	}, "p118-run-job")
	if err != nil {
		t.Fatalf("run job: %v", err)
	}
	if acceptedJob.JobID != p118JobID || acceptedJob.SessionID != p118SessionID || acceptedJob.CommandID != p118CommandID {
		t.Fatalf("job acceptance=%+v", acceptedJob)
	}
	job, err := client.GetJob(ctx, p118JobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.Resource.JobID != p118JobID {
		t.Fatalf("job snapshot=%+v", job.Resource)
	}
	if fixture.view == "local_intent" {
		if job.Resource.EffectivePhase() != "" || job.Resource.DeliveryState != "recorded" || job.Resource.TeardownState != "" {
			t.Fatalf("local job intent invented target state: %+v", job.Resource)
		}
	} else if job.Resource.EffectivePhase() != "complete" || job.Resource.TeardownState != "closed" {
		t.Fatalf("direct job authority snapshot=%+v", job.Resource)
	}

	if _, err := client.StreamCommandEvents(ctx, "expired-command", 4, false); err == nil {
		t.Fatal("expired event history unexpectedly succeeded")
	} else {
		var apiError *APIError
		if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusGone || apiError.Code != "event_history_unavailable" || apiError.ResourceID != "expired-command" {
			t.Fatalf("event history error=%#v", err)
		}
		details := apiError.EventHistoryDetails()
		if details.OutputComplete == nil || *details.OutputComplete || details.OutputUnavailableReason != "remote_event_gap" || details.EarliestAvailable == nil || *details.EarliestAvailable != 8 {
			t.Fatalf("event history details=%+v", details)
		}
	}

	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.records) != 10 {
		t.Fatalf("request count=%d, want ten: %+v", len(fixture.records), fixture.records)
	}
	for _, request := range fixture.records {
		if request.method == http.MethodPost || request.method == http.MethodDelete {
			if request.idempotencyKey == "" {
				t.Errorf("mutation %s %s omitted Idempotency-Key", request.method, request.path)
			}
		}
	}
}

func TestP118SameOpaqueIDNeverSelectsIngress(t *testing.T) {
	unixFixture := &p118Fixture{view: "local_intent"}
	httpsFixture := &p118Fixture{view: "authority"}
	unixClient := p118NewUnixClient(t, unixFixture)
	httpsClient := p118NewHTTPSClient(t, httpsFixture)
	t.Cleanup(unixClient.CloseIdleConnections)
	t.Cleanup(httpsClient.CloseIdleConnections)

	for _, client := range []*Client{unixClient, httpsClient} {
		snapshot, err := client.GetSession(context.Background(), "same-id")
		if err != nil {
			t.Fatalf("get same opaque ID over %s: %v", client.EndpointKind(), err)
		}
		if snapshot.Resource.SessionID != "same-id" {
			t.Fatalf("%s returned ID %q", client.EndpointKind(), snapshot.Resource.SessionID)
		}
	}
	if unixClient.EndpointKind() != EndpointUnixSocket || httpsClient.EndpointKind() != EndpointHTTPS {
		t.Fatalf("client ingress changed: Unix=%q HTTPS=%q", unixClient.EndpointKind(), httpsClient.EndpointKind())
	}
	unixFixture.mu.Lock()
	unixCount := len(unixFixture.records)
	unixPath := ""
	if unixCount > 0 {
		unixPath = unixFixture.records[0].path
	}
	unixFixture.mu.Unlock()
	httpsFixture.mu.Lock()
	httpsCount := len(httpsFixture.records)
	httpsPath := ""
	if httpsCount > 0 {
		httpsPath = httpsFixture.records[0].path
	}
	httpsFixture.mu.Unlock()
	if unixCount != 1 || httpsCount != 1 {
		t.Fatalf("requests were not isolated to the selected ingress: Unix=%+v HTTPS=%+v", unixFixture.records, httpsFixture.records)
	}
	if unixPath != "/v1/sessions/same-id" || httpsPath != "/v1/sessions/same-id" {
		t.Fatalf("opaque ID paths differ: Unix=%q HTTPS=%q", unixPath, httpsPath)
	}
}

func TestP118EventCursorRejectsWrongIdentityGapAndBadPayload(t *testing.T) {
	for _, testCase := range []struct {
		name string
		line string
	}{
		{name: "wrong command", line: `{"command_id":"other","sequence":3,"type":"stdout","timestamp":"2026-09-28T00:00:00Z","encoding":"base64","data_base64":"AQ==","byte_count":1}`},
		{name: "sequence gap", line: `{"command_id":"same-opaque-command-id","sequence":4,"type":"stdout","timestamp":"2026-09-28T00:00:00Z","encoding":"base64","data_base64":"AQ==","byte_count":1}`},
		{name: "invalid base64", line: `{"command_id":"same-opaque-command-id","sequence":3,"type":"stdout","timestamp":"2026-09-28T00:00:00Z","encoding":"base64","data_base64":"!","byte_count":1}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stream := p118EventStreamForLine(t, testCase.line, "same-opaque-command-id", 2)
			if _, err := stream.Next(); !errors.Is(err, ErrProtocol) {
				t.Fatalf("Next error=%v, want ErrProtocol", err)
			}
			if stream.Cursor() != 2 {
				t.Fatalf("invalid frame advanced cursor to %d", stream.Cursor())
			}
		})
	}
}

func TestP118EventCursorChecksServerResumeBoundary(t *testing.T) {
	line := `{"command_id":"cmd","sequence":3,"type":"stdout","timestamp":"2026-09-28T00:00:00Z","encoding":"base64","data_base64":"AQ==","byte_count":1}`
	stream := p118EventStreamForLine(t, line, "cmd", 2)
	stream.response.Header.Set("X-Runner-Last-Sequence", "4")
	if event, err := stream.Next(); err != nil || event.Sequence != 3 {
		t.Fatalf("read event=%+v err=%v", event, err)
	}
	if _, err := stream.Next(); !errors.Is(err, ErrProtocol) {
		t.Fatalf("end-of-stream cursor error=%v, want ErrProtocol", err)
	}
	if stream.Cursor() != 3 {
		t.Fatalf("server boundary mismatch changed delivered cursor to %d", stream.Cursor())
	}

	missingBoundary := p118EventStreamForLine(t, line, "cmd", 2)
	if _, err := missingBoundary.Next(); err != nil {
		t.Fatalf("read event before missing-boundary check: %v", err)
	}
	if _, err := missingBoundary.Next(); !errors.Is(err, ErrProtocol) {
		t.Fatalf("missing server cursor error=%v, want ErrProtocol", err)
	}
}

func TestP118ConstructorAndTransportFailuresAreTyped(t *testing.T) {
	if _, err := NewUnixSocketClient("relative.sock"); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("relative Unix socket error=%v", err)
	}
	if _, err := NewUnixSocketClient("/tmp/../runner.sock"); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("unclean Unix socket error=%v", err)
	}
	if _, err := NewHTTPSClient(HTTPSConfig{Endpoint: "http://127.0.0.1:8443"}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("non-HTTPS endpoint error=%v", err)
	}
	if _, err := NewHTTPSClient(HTTPSConfig{Endpoint: "https://user:pass@example.test"}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("credential-bearing endpoint error=%v", err)
	}
	if _, err := NewHTTPSClient(HTTPSConfig{Endpoint: "https://example.test/api", RootCAs: x509.NewCertPool()}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("path-bearing endpoint error=%v", err)
	}
	if _, err := NewHTTPSClient(HTTPSConfig{Endpoint: "https://example.test", RootCAs: x509.NewCertPool()}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("missing client certificate error=%v", err)
	}

	client, err := NewUnixSocketClient(t.TempDir() + "/missing.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if _, err := client.GetSession(context.Background(), "opaque-id"); !errors.Is(err, ErrTransport) {
		t.Fatalf("missing socket error=%v, want ErrTransport", err)
	}
	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.GetSession(cancelledContext, "opaque-id"); !errors.Is(err, ErrTransport) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled transport error=%v, want ErrTransport and context.Canceled", err)
	}
}

func TestP118HTTPSRejectsTLS12OnlyServer(t *testing.T) {
	serverCertificate, clientCertificate, roots := p118MTLSCertificates(t)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		p118WriteJSON(response, http.StatusOK, map[string]any{"view": "authority", "is_stale": false, "resource": map[string]any{"session_id": "opaque-id"}})
	}))
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		MaxVersion:   tls.VersionTLS12,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    roots,
		Certificates: []tls.Certificate{serverCertificate},
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	client, err := NewHTTPSClient(HTTPSConfig{Endpoint: server.URL, RootCAs: roots, ClientCertificate: clientCertificate})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if _, err := client.GetSession(context.Background(), "opaque-id"); !errors.Is(err, ErrTransport) {
		t.Fatalf("TLS 1.2-only server error=%v, want ErrTransport", err)
	}
}

func TestP118ResourceAndIdempotencyValidation(t *testing.T) {
	client, err := NewUnixSocketClient("/tmp/runnerclient-test.sock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetSession(context.Background(), "../other"); !errors.Is(err, ErrResourceID) {
		t.Fatalf("path traversal ID error=%v", err)
	}
	if _, err := client.CreateSession(context.Background(), CreateSessionRequest{}, " \t"); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("empty idempotency key error=%v", err)
	}
}

type p118Fixture struct {
	view    string
	mu      sync.Mutex
	records []p118RequestRecord
}

type p118RequestRecord struct {
	method         string
	path           string
	query          string
	idempotencyKey string
	body           []byte
}

func (f *p118Fixture) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	_ = request.Body.Close()
	record := p118RequestRecord{
		method: request.Method, path: request.URL.Path, query: request.URL.RawQuery,
		idempotencyKey: request.Header.Get("Idempotency-Key"), body: body,
	}
	f.mu.Lock()
	f.records = append(f.records, record)
	f.mu.Unlock()

	response.Header().Set("Content-Type", "application/json")
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/v1/sessions":
		var input CreateSessionRequest
		if json.Unmarshal(body, &input) != nil || input.ExecutionTarget.Kind != "remote" || len(input.Source) == 0 {
			http.Error(response, "invalid session request", http.StatusBadRequest)
			return
		}
		p118WriteJSON(response, http.StatusAccepted, Acceptance{ResourceID: p118SessionID, SessionID: p118SessionID, AcceptanceScope: "local_intent", ExecutionTarget: input.ExecutionTarget, KnownState: KnownState{DeliveryState: "recorded"}})
	case request.Method == http.MethodGet && request.URL.Path == "/v1/sessions/"+p118SessionID:
		resource := map[string]any{
			"session_id": p118SessionID, "execution_target": Target{Kind: "remote", Profile: "linux-host"},
			"controller": Controller{Type: "local_user", ID: "tomasz.walczuk"}, "observed_at": "2026-09-28T00:00:00Z",
			"environment": "linux-dev", "source": Source{Mode: "empty"},
		}
		if f.view == "local_intent" {
			resource["delivery_state"] = "recorded"
		} else {
			resource["session_state"] = "ready"
			resource["authority"] = "linux"
		}
		p118WriteJSON(response, http.StatusOK, map[string]any{"view": f.view, "is_stale": f.view == "local_intent", "resource": resource})
	case request.Method == http.MethodPost && request.URL.Path == "/v1/sessions/"+p118SessionID+"/commands":
		var input SubmitCommandRequest
		if json.Unmarshal(body, &input) != nil || input.Script == "" || strings.Contains(string(body), "execution_target") {
			http.Error(response, "invalid submit request", http.StatusBadRequest)
			return
		}
		p118WriteJSON(response, http.StatusAccepted, Acceptance{ResourceID: p118CommandID, SessionID: p118SessionID, CommandID: p118CommandID, AcceptanceScope: "local_intent", ExecutionTarget: Target{Kind: "remote", Profile: "linux-host"}, KnownState: KnownState{DeliveryState: "recorded"}})
	case request.Method == http.MethodGet && request.URL.Path == "/v1/commands/"+p118CommandID:
		resource := map[string]any{"command_id": p118CommandID, "session_id": p118SessionID, "execution_target": Target{Kind: "remote", Profile: "linux-host"}, "observed_at": "2026-09-28T00:00:00Z"}
		if f.view == "local_intent" {
			resource["delivery_state"] = "recorded"
		} else {
			resource["command_state"] = "succeeded"
			resource["ordinal"] = 1
			resource["output_complete"] = true
		}
		p118WriteJSON(response, http.StatusOK, map[string]any{"view": f.view, "is_stale": false, "resource": resource})
	case request.Method == http.MethodGet && request.URL.Path == "/v1/commands/"+p118CommandID+"/events":
		if request.URL.Query().Get("after") != "2" || request.URL.Query().Get("follow") != "true" {
			http.Error(response, "event query mismatch", http.StatusBadRequest)
			return
		}
		response.Header().Set("Content-Type", "application/x-ndjson")
		response.Header().Set("Trailer", "X-Runner-Last-Sequence")
		_, _ = fmt.Fprintln(response, `{"command_id":"same-opaque-command-id","sequence":3,"type":"stdout","timestamp":"2026-09-28T00:00:00Z","encoding":"base64","data_base64":"AG91dA==","byte_count":4}`)
		_, _ = fmt.Fprintln(response, `{"command_id":"same-opaque-command-id","sequence":4,"type":"command_succeeded","timestamp":"2026-09-28T00:00:01Z"}`)
		response.Header().Set("X-Runner-Last-Sequence", "4")
	case request.Method == http.MethodGet && request.URL.Path == "/v1/commands/expired-command/events":
		p118WriteJSON(response, http.StatusGone, map[string]any{
			"code": "event_history_unavailable", "message": "event history unavailable", "retryable": false,
			"resource_id": "expired-command", "details": map[string]any{
				"output_complete": false, "output_unavailable_reason": "remote_event_gap", "earliest_available_sequence": 8,
			},
		})
	case request.Method == http.MethodPost && request.URL.Path == "/v1/commands/"+p118CommandID+"/cancel":
		if len(body) != 0 {
			http.Error(response, "cancel must have an empty body", http.StatusBadRequest)
			return
		}
		p118WriteJSON(response, http.StatusAccepted, Acceptance{ResourceID: p118CommandID, SessionID: p118SessionID, CommandID: p118CommandID, AcceptanceScope: "local_intent"})
	case request.Method == http.MethodDelete && request.URL.Path == "/v1/sessions/"+p118SessionID:
		var input struct {
			Policy string `json:"policy"`
		}
		if json.Unmarshal(body, &input) != nil || input.Policy != "graceful" {
			http.Error(response, "close policy mismatch", http.StatusBadRequest)
			return
		}
		p118WriteJSON(response, http.StatusAccepted, Acceptance{ResourceID: p118SessionID, SessionID: p118SessionID, AcceptanceScope: "local_intent"})
	case request.Method == http.MethodPost && request.URL.Path == "/v1/jobs":
		var input RunJobRequest
		if json.Unmarshal(body, &input) != nil || input.Script == "" || input.ExecutionTarget.Kind != "remote" {
			http.Error(response, "invalid run request", http.StatusBadRequest)
			return
		}
		p118WriteJSON(response, http.StatusAccepted, Acceptance{ResourceID: p118JobID, JobID: p118JobID, SessionID: p118SessionID, CommandID: p118CommandID, AcceptanceScope: "local_intent"})
	case request.Method == http.MethodGet && request.URL.Path == "/v1/jobs/"+p118JobID:
		resource := map[string]any{
			"job_id": p118JobID, "session_id": p118SessionID, "command_id": p118CommandID,
			"execution_target": Target{Kind: "remote", Profile: "linux-host"}, "observed_at": "2026-09-28T00:00:00Z",
		}
		if f.view == "authority" {
			resource["phase"] = "complete"
			resource["teardown_state"] = "closed"
		} else {
			resource["delivery_state"] = "recorded"
		}
		p118WriteJSON(response, http.StatusOK, map[string]any{"view": f.view, "is_stale": false, "resource": resource})
	case request.Method == http.MethodGet && request.URL.Path == "/v1/commands/gap/events":
		response.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintln(response, `{"command_id":"same-opaque-command-id","sequence":4,"type":"stdout","timestamp":"2026-09-28T00:00:00Z","encoding":"base64","data_base64":"AQ==","byte_count":1}`)
	case request.Method == http.MethodGet && request.URL.Path == "/v1/commands/wrong-command/events":
		response.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintln(response, `{"command_id":"other","sequence":3,"type":"stdout","timestamp":"2026-09-28T00:00:00Z","encoding":"base64","data_base64":"AQ==","byte_count":1}`)
	case request.Method == http.MethodGet && request.URL.Path == "/v1/commands/bad-base64/events":
		response.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintln(response, `{"command_id":"same-opaque-command-id","sequence":3,"type":"stdout","timestamp":"2026-09-28T00:00:00Z","encoding":"base64","data_base64":"!","byte_count":1}`)
	case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/sessions/"):
		id := path.Base(request.URL.Path)
		p118WriteJSON(response, http.StatusOK, map[string]any{"view": f.view, "is_stale": false, "resource": map[string]any{"session_id": id}})
	default:
		http.Error(response, "unexpected contract request: "+request.Method+" "+request.URL.Path, http.StatusNotFound)
	}
}

func p118WriteJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func p118NewUnixClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	socketDir, err := os.MkdirTemp("/tmp", "p118-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := socketDir + "/api.sock"
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	client, err := NewUnixSocketClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func p118NewHTTPSClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	serverCertificate, clientCertificate, roots := p118MTLSCertificates(t)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    roots,
		Certificates: []tls.Certificate{serverCertificate},
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewHTTPSClient(HTTPSConfig{Endpoint: server.URL, RootCAs: roots, ClientCertificate: clientCertificate})
	if err != nil {
		t.Fatal(err)
	}
	if client.endpointURL != "https://"+parsed.Host {
		t.Fatalf("normalized HTTPS endpoint=%q, want origin %q", client.endpointURL, "https://"+parsed.Host)
	}
	return client
}

func p118MTLSCertificates(t *testing.T) (tls.Certificate, tls.Certificate, *x509.CertPool) {
	t.Helper()
	now := time.Now()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)

	makeLeaf := func(serial int64, usage x509.ExtKeyUsage) tls.Certificate {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(serial), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(12 * time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage},
			DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	}
	return makeLeaf(2, x509.ExtKeyUsageServerAuth), makeLeaf(3, x509.ExtKeyUsageClientAuth), roots
}

func p118EventStreamForLine(t *testing.T, line, commandID string, cursor int64) *EventStream {
	t.Helper()
	response := &http.Response{
		Header: http.Header{"Content-Type": []string{"application/x-ndjson"}},
		Body:   io.NopCloser(strings.NewReader(line + "\n")),
	}
	stream := &EventStream{body: response.Body, response: response, commandID: commandID, cursor: cursor}
	stream.scanner = bufio.NewScanner(response.Body)
	return stream
}

func TestP118HTTPSDoesNotFollowRedirects(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Store(true) }))
	t.Cleanup(target.Close)
	fixture := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Location", target.URL+"/v1/sessions/redirected")
		response.WriteHeader(http.StatusTemporaryRedirect)
	})
	client := p118NewHTTPSClient(t, fixture)
	defer client.CloseIdleConnections()
	_, err := client.GetSession(context.Background(), "opaque-id")
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("redirect response error=%v", err)
	}
	if redirected.Load() {
		t.Fatal("HTTPS client followed a redirect to a different endpoint")
	}
}

func TestP118OutputEventBytesAreNotReencoded(t *testing.T) {
	const raw = "\x00\xff\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(raw))
	line := fmt.Sprintf(`{"command_id":"cmd","sequence":1,"type":"stdout","timestamp":"2026-09-28T00:00:00Z","encoding":"base64","data_base64":%q,"byte_count":%d}`, encoded, len(raw))
	stream := p118EventStreamForLine(t, line, "cmd", 0)
	event, err := stream.Next()
	if err != nil {
		t.Fatal(err)
	}
	if string(event.Data) != raw || event.DataBase64 != encoded || event.ByteCount != int64(len(raw)) {
		t.Fatalf("output bytes changed: event=%+v data=%v", event, event.Data)
	}
}
