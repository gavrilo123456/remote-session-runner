package runnerd

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

const p110RunBody = `{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"printf p110"}`

func TestP110DirectRunGetJobPreservesCommandAndTeardownOutcome(t *testing.T) {
	runtimeAdapter := &p048FakeRuntime{generation: "p110-teardown-generation", stopConfirmed: false}
	service, _ := newP046Service(t, runtimeAdapter)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")
	created := p107Do(handler, controller, true, http.MethodPost, "/v1/jobs", []byte(p110RunBody), "p110-teardown-key")
	if created.Code != http.StatusAccepted {
		t.Fatalf("run status = %d, body = %s", created.Code, created.Body.String())
	}
	var acceptance directJobAcceptance
	p107Decode(t, created, &acceptance)
	if acceptance.ResourceID == "" || acceptance.ResourceID != acceptance.JobID || acceptance.SessionID == "" || acceptance.CommandID == "" ||
		acceptance.AcceptanceScope != "target_authority" || acceptance.ExecutionTarget.Kind != string(domain.TargetKindRemote) ||
		acceptance.ExecutionTarget.Profile != "linux-host" || acceptance.KnownState.CommandState != string(domain.CommandStateSucceeded) {
		t.Fatalf("job acceptance = %#v", acceptance)
	}

	read := p107Do(handler, controller, true, http.MethodGet, "/v1/jobs/"+acceptance.JobID, nil, "")
	if read.Code != http.StatusOK {
		t.Fatalf("get-job status = %d, body = %s", read.Code, read.Body.String())
	}
	var snapshot directJobReadResponse
	p107Decode(t, read, &snapshot)
	job := snapshot.Resource
	if snapshot.View != "authority" || snapshot.IsStale || job.JobID != acceptance.JobID || job.SessionID != acceptance.SessionID ||
		job.CommandID != acceptance.CommandID || job.Phase != "lost" || job.CommandState == nil || *job.CommandState != string(domain.CommandStateSucceeded) ||
		job.ExitCode == nil || *job.ExitCode != 0 || !job.OutputComplete || job.TeardownState != "lost" ||
		job.Authority != "remote" || job.Controller.ID != "tomasz.walczuk" || job.Capabilities.EffectiveAccount != "ubuntu" {
		t.Fatalf("job snapshot failed to preserve the command and teardown result: %#v", snapshot)
	}
	if strings.Contains(read.Body.String(), "printf p110") || strings.Contains(read.Body.String(), "script") {
		t.Fatalf("job read exposed its stored script: %s", read.Body.String())
	}
	if runtimeAdapter.commandCalls != 1 || runtimeAdapter.stopCalls != 1 {
		t.Fatalf("runtime command calls=%d stop calls=%d, want one each", runtimeAdapter.commandCalls, runtimeAdapter.stopCalls)
	}

	otherController := p107DirectController(t, "other-controller")
	denied := p107Do(handler, otherController, true, http.MethodGet, "/v1/jobs/"+acceptance.JobID, nil, "")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("cross-controller job read status = %d, body = %s", denied.Code, denied.Body.String())
	}
	missing := p107Do(handler, controller, true, http.MethodGet, "/v1/jobs/job-p110-missing", nil, "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing job read status = %d, body = %s", missing.Code, missing.Body.String())
	}
}

func TestP110DirectRunIdempotencyReusesJobAndDoesNotRerun(t *testing.T) {
	runtimeAdapter := &p048FakeRuntime{generation: "p110-replay-generation", stopConfirmed: true}
	service, _ := newP046Service(t, runtimeAdapter)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	controller := p107DirectController(t, "tomasz.walczuk")
	first := p107Do(handler, controller, true, http.MethodPost, "/v1/jobs", []byte(p110RunBody), "p110-replay-key")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first run status = %d, body = %s", first.Code, first.Body.String())
	}
	var firstAcceptance directJobAcceptance
	p107Decode(t, first, &firstAcceptance)
	second := p107Do(handler, controller, true, http.MethodPost, "/v1/jobs", []byte(p110RunBody), "p110-replay-key")
	if second.Code != http.StatusAccepted {
		t.Fatalf("same-key replay status = %d, body = %s", second.Code, second.Body.String())
	}
	var secondAcceptance directJobAcceptance
	p107Decode(t, second, &secondAcceptance)
	if secondAcceptance.JobID != firstAcceptance.JobID || secondAcceptance.SessionID != firstAcceptance.SessionID || secondAcceptance.CommandID != firstAcceptance.CommandID {
		t.Fatalf("same-key replay changed stable job IDs: first=%#v second=%#v", firstAcceptance, secondAcceptance)
	}
	changedBody := strings.Replace(p110RunBody, "printf p110", "printf changed", 1)
	conflict := p107Do(handler, controller, true, http.MethodPost, "/v1/jobs", []byte(changedBody), "p110-replay-key")
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed-payload replay status = %d, body = %s", conflict.Code, conflict.Body.String())
	}
	if runtimeAdapter.commandCalls != 1 || runtimeAdapter.stopCalls != 1 {
		t.Fatalf("replay reran work: runtime command calls=%d stop calls=%d", runtimeAdapter.commandCalls, runtimeAdapter.stopCalls)
	}
}

func TestP110LoopbackTLS13MTLSRunsAndReadsJob(t *testing.T) {
	runtimeAdapter := &p048FakeRuntime{generation: "p110-mtls-generation", stopConfirmed: true}
	service, _ := newP046Service(t, runtimeAdapter)
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
	endpoint := "https://" + server.Addr() + "/v1/jobs"
	noCertificateClient := p106HTTPClient(pki.serverRoots, nil, tls.VersionTLS13)
	unauthenticatedRequest, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(p110RunBody))
	if err != nil {
		t.Fatal(err)
	}
	unauthenticatedRequest.Header.Set("Idempotency-Key", "p110-no-client-cert")
	if response, err := noCertificateClient.Do(unauthenticatedRequest); err == nil {
		response.Body.Close()
		t.Fatalf("request without a client certificate unexpectedly reached the API with HTTP %d", response.StatusCode)
	}
	if runtimeAdapter.commandCalls != 0 || runtimeAdapter.stopCalls != 0 {
		t.Fatalf("request without mTLS mutated the runtime: commands=%d stops=%d", runtimeAdapter.commandCalls, runtimeAdapter.stopCalls)
	}

	client := p106HTTPClient(pki.serverRoots, &pki.clientTLS, tls.VersionTLS13)
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(p110RunBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "p110-mtls-run")
	created, err := client.Do(request)
	if err != nil {
		t.Fatalf("mTLS job run: %v", err)
	}
	defer created.Body.Close()
	if created.StatusCode != http.StatusAccepted || created.TLS == nil || created.TLS.Version != tls.VersionTLS13 {
		t.Fatalf("mTLS job run status=%d TLS=%v body=%s", created.StatusCode, created.TLS, p107HTTPBody(t, created))
	}
	var acceptance directJobAcceptance
	if err := json.NewDecoder(created.Body).Decode(&acceptance); err != nil {
		t.Fatal(err)
	}
	readRequest, err := http.NewRequest(http.MethodGet, "https://"+server.Addr()+"/v1/jobs/"+acceptance.JobID, nil)
	if err != nil {
		t.Fatal(err)
	}
	read, err := client.Do(readRequest)
	if err != nil {
		t.Fatalf("mTLS get-job: %v", err)
	}
	defer read.Body.Close()
	if read.StatusCode != http.StatusOK {
		t.Fatalf("mTLS get-job status=%d body=%s", read.StatusCode, p107HTTPBody(t, read))
	}
	var snapshot directJobReadResponse
	if err := json.NewDecoder(read.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Resource.JobID != acceptance.JobID || snapshot.Resource.TeardownState != "closed" || snapshot.Resource.CommandState == nil || *snapshot.Resource.CommandState != string(domain.CommandStateSucceeded) {
		t.Fatalf("mTLS job snapshot = %#v", snapshot)
	}
}

func TestP110DirectRunOversizeInputsCreateNoJob(t *testing.T) {
	runtimeAdapter := &p048FakeRuntime{generation: "p110-limit-generation", stopConfirmed: true}
	service, authority := newP046Service(t, runtimeAdapter)
	handler, err := NewDirectHTTPSAPIHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	api := handler.(*directHTTPSAPI)
	allocations := 0
	api.allocateRunIDs = func() (domain.JobID, domain.SessionID, domain.CommandID, error) {
		allocations++
		return newDirectRunIDs()
	}
	controller := p107DirectController(t, "tomasz.walczuk")
	conflictingTimeout := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"script":"printf invalid","timeout_seconds":30,"limits":{"command_timeout_seconds":31}}`)
	timeoutResponse := p107Do(handler, controller, true, http.MethodPost, "/v1/jobs", conflictingTimeout, "p110-timeout-conflict")
	if timeoutResponse.Code != http.StatusUnprocessableEntity {
		t.Fatalf("conflicting timeout status = %d, want 422; body = %s", timeoutResponse.Code, timeoutResponse.Body.String())
	}

	oversizeBody := bytes.Repeat([]byte{'x'}, domain.MaxSerializedRequestBytes+1)
	bodyResponse := p107Do(handler, controller, true, http.MethodPost, "/v1/jobs", oversizeBody, "p110-body-limit")
	if bodyResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body status = %d, want 413; body = %s", bodyResponse.Code, bodyResponse.Body.String())
	}

	oversizeScriptBody := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"script":"` + strings.Repeat("x", domain.MaxScriptUTF8Bytes+1) + `"}`)
	scriptResponse := p107Do(handler, controller, true, http.MethodPost, "/v1/jobs", oversizeScriptBody, "p110-script-limit")
	if scriptResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize script status = %d, want 413; body = %s", scriptResponse.Code, scriptResponse.Body.String())
	}
	if allocations != 0 || runtimeAdapter.commandCalls != 0 || runtimeAdapter.stopCalls != 0 {
		t.Fatalf("oversize requests reached job allocation or runtime: allocations=%d commands=%d stops=%d", allocations, runtimeAdapter.commandCalls, runtimeAdapter.stopCalls)
	}
	sessions, err := authority.ListSessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("oversize requests created authoritative sessions: %#v", sessions)
	}
}
