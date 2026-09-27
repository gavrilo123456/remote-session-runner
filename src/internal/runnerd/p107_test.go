package runnerd

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

const p107CreateBody = `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"}}`

func TestP107DirectSessionCreateReadAndReplay(t *testing.T) {
	controller := p107DirectController(t, "tomasz.walczuk")
	service, authority := newP046Service(t, &p046FakeRuntime{generation: "p107-generation"})
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	missing := p107Do(handler, controller, true, http.MethodGet, "/v1/sessions/sess-p107-missing", nil, "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing session status = %d, body = %s", missing.Code, missing.Body.String())
	}
	var missingError directAPIError
	p107Decode(t, missing, &missingError)
	if missingError.Code != "resource_not_found" {
		t.Fatalf("missing session error = %#v", missingError)
	}

	created := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", []byte(p107CreateBody), "p107-key")
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var acceptance directSessionAcceptance
	p107Decode(t, created, &acceptance)
	if acceptance.SessionID == "" || acceptance.ResourceID != acceptance.SessionID || acceptance.AcceptanceScope != "target_authority" ||
		acceptance.ExecutionTarget.Kind != string(domain.TargetKindRemote) || acceptance.ExecutionTarget.Profile != "linux-host" ||
		acceptance.KnownState.SessionState != string(domain.SessionStateReady) {
		t.Fatalf("create acceptance = %#v", acceptance)
	}

	replayBody := []byte(`{"execution_target":{"profile":"linux-host","kind":"remote"},"environment":"linux-dev","limits":{},"policy":{}}`)
	replay := p107Do(handler, controller, true, http.MethodPost, "/v1/sessions", replayBody, "p107-key")
	if replay.Code != http.StatusAccepted {
		t.Fatalf("same-key replay status = %d, body = %s", replay.Code, replay.Body.String())
	}
	var replayAcceptance directSessionAcceptance
	p107Decode(t, replay, &replayAcceptance)
	if replayAcceptance.SessionID != acceptance.SessionID {
		t.Fatalf("same-key replay session = %q, want original %q", replayAcceptance.SessionID, acceptance.SessionID)
	}

	read := p107Do(handler, controller, true, http.MethodGet, "/v1/sessions/"+acceptance.SessionID, nil, "")
	if read.Code != http.StatusOK {
		t.Fatalf("read status = %d, body = %s", read.Code, read.Body.String())
	}
	var snapshot directSessionReadResponse
	p107Decode(t, read, &snapshot)
	resource := snapshot.Resource
	if snapshot.View != "authority" || snapshot.IsStale || resource.SessionID != acceptance.SessionID ||
		resource.SessionState != string(domain.SessionStateReady) || resource.Authority != "remote" ||
		resource.ExecutionTarget.Kind != string(domain.TargetKindRemote) || resource.Environment != "linux-dev" ||
		resource.Controller.Type != string(domain.ControllerTypeDirectMTLS) || resource.Controller.ID != "tomasz.walczuk" ||
		resource.Source.Mode != string(domain.SourceModeEmpty) || resource.Capabilities.EffectiveAccount != "ubuntu" ||
		resource.Capabilities.Isolation != string(domain.IsolationOSUser) {
		t.Fatalf("session snapshot = %#v", snapshot)
	}
	if resource.Capabilities.ServiceLimits == nil || len(resource.Capabilities.ServiceLimits) == 0 {
		t.Fatalf("session snapshot has no effective service limits: %#v", resource.Capabilities)
	}

	otherController := p107DirectController(t, "other-controller")
	deniedRead := p107Do(handler, otherController, true, http.MethodGet, "/v1/sessions/"+acceptance.SessionID, nil, "")
	if deniedRead.Code != http.StatusForbidden {
		t.Fatalf("cross-controller read status = %d, body = %s", deniedRead.Code, deniedRead.Body.String())
	}
	var denial directAPIError
	p107Decode(t, deniedRead, &denial)
	if denial.Code != "controller_mismatch" {
		t.Fatalf("cross-controller error = %#v", denial)
	}

	sessions, err := authority.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != domain.SessionID(acceptance.SessionID) {
		t.Fatalf("authoritative sessions after replay/read denial = %#v, want one original resource", sessions)
	}
	if reservations, err := authority.CountLiveSessionReservations(context.Background()); err != nil || reservations != 1 {
		t.Fatalf("live reservations after accepted create and replay = %d, err = %v, want 1", reservations, err)
	}
}

func TestP107DirectSessionRoutesUseMappedMTLSPrincipal(t *testing.T) {
	service, _ := newP046Service(t, &p046FakeRuntime{generation: "p107-mtls-generation"})
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
	createRequest.Header.Set("Idempotency-Key", "p107-loopback-key")
	created, err := client.Do(createRequest)
	if err != nil {
		t.Fatalf("mTLS create request: %v", err)
	}
	defer created.Body.Close()
	if created.StatusCode != http.StatusAccepted {
		t.Fatalf("mTLS create status = %d, body = %s", created.StatusCode, p107HTTPBody(t, created))
	}
	var acceptance directSessionAcceptance
	if err := json.NewDecoder(created.Body).Decode(&acceptance); err != nil {
		t.Fatalf("decode mTLS create acceptance: %v", err)
	}
	if acceptance.SessionID == "" || acceptance.AcceptanceScope != "target_authority" {
		t.Fatalf("mTLS create acceptance = %#v", acceptance)
	}

	readURL := "https://" + server.Addr() + "/v1/sessions/" + acceptance.SessionID
	read, err := client.Get(readURL)
	if err != nil {
		t.Fatalf("mTLS session read: %v", err)
	}
	defer read.Body.Close()
	if read.StatusCode != http.StatusOK {
		t.Fatalf("mTLS session read status = %d, body = %s", read.StatusCode, p107HTTPBody(t, read))
	}
	var snapshot directSessionReadResponse
	if err := json.NewDecoder(read.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode mTLS session read: %v", err)
	}
	if snapshot.View != "authority" || snapshot.Resource.Controller.ID != "tomasz.walczuk" || snapshot.Resource.SessionID != acceptance.SessionID {
		t.Fatalf("mTLS session snapshot = %#v", snapshot)
	}
}

func TestP107DirectSessionDenialsDoNotCreateAuthorityResources(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal string
		hasCaller bool
		body      []byte
		wantCode  int
	}{
		{name: "local target", principal: "tomasz.walczuk", hasCaller: true, body: []byte(`{"environment":"linux-dev","execution_target":{"kind":"local","profile":"mac-workstation"}}`), wantCode: http.StatusUnprocessableEntity},
		{name: "environment controller denial", principal: "not-allowed", hasCaller: true, body: []byte(p107CreateBody), wantCode: http.StatusForbidden},
		{name: "controller cannot be supplied in body", principal: "tomasz.walczuk", hasCaller: true, body: []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"controller":{"controller_type":"direct_mtls","controller_id":"not-allowed"}}`), wantCode: http.StatusBadRequest},
		{name: "unknown environment", principal: "tomasz.walczuk", hasCaller: true, body: []byte(`{"environment":"not-configured","execution_target":{"kind":"remote","profile":"linux-host"}}`), wantCode: http.StatusUnprocessableEntity},
		{name: "malformed body", principal: "tomasz.walczuk", hasCaller: true, body: []byte(`{"environment":`), wantCode: http.StatusBadRequest},
		{name: "oversize body", principal: "tomasz.walczuk", hasCaller: true, body: []byte(strings.Repeat(" ", domain.MaxSerializedRequestBytes+1)), wantCode: http.StatusRequestEntityTooLarge},
		{name: "missing mapped principal", body: []byte(p107CreateBody), wantCode: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			var controller domain.ControllerIdentity
			if test.hasCaller {
				controller = p107DirectController(t, test.principal)
			}
			service, authority := newP046Service(t, &p046FakeRuntime{generation: "p107-denial-generation"})
			handler, err := NewDirectHTTPSAPIHandler(service)
			if err != nil {
				t.Fatal(err)
			}
			response := p107Do(handler, controller, test.hasCaller, http.MethodPost, "/v1/sessions", test.body, "p107-denial-key")
			if response.Code != test.wantCode {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantCode, response.Body.String())
			}
			sessions, err := authority.ListSessions(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(sessions) != 0 {
				t.Fatalf("denied request created authoritative resources: %#v", sessions)
			}
			if reservations, err := authority.CountLiveSessionReservations(context.Background()); err != nil || reservations != 0 {
				t.Fatalf("denied request left %d live capacity reservations, err = %v", reservations, err)
			}
		})
	}
}

func p107DirectController(t *testing.T, id string) domain.ControllerIdentity {
	t.Helper()
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, domain.ControllerID(id))
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func p107Do(handler http.Handler, controller domain.ControllerIdentity, hasCaller bool, method, path string, body []byte, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if len(body) > 0 {
		request.ContentLength = -1
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if hasCaller {
		request = request.WithContext(context.WithValue(request.Context(), directPrincipalContextKey{}, DirectPrincipal{Controller: controller}))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func p107Decode(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
}

func p107HTTPBody(t *testing.T, response *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read HTTP body: %v", err)
	}
	return string(body)
}
