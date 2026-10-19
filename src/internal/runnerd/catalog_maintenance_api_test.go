package runnerd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/lifecycle"
)

type directCatalogProbeGuardFixture struct {
	operationID string
	controller  string
	token       string
	key         string
	body        []byte
	jobID       string
	reserved    bool
	accepted    bool
}

func (g *directCatalogProbeGuardFixture) OperationID() string { return g.operationID }
func (g *directCatalogProbeGuardFixture) ReserveCatalogMaintenanceProbe(controller, token, key string, body []byte, now time.Time) error {
	if g.reserved || controller != g.controller || token != g.token || key != g.key || !bytes.Equal(body, g.body) || now.IsZero() {
		return ErrDirectHTTPSAPIConfiguration
	}
	g.reserved = true
	return nil
}
func (g *directCatalogProbeGuardFixture) BindCatalogMaintenanceProbeJob(jobID string) error {
	if !g.reserved || g.jobID != "" || !strings.HasPrefix(jobID, "job-") {
		return ErrDirectHTTPSAPIConfiguration
	}
	g.jobID = jobID
	return nil
}
func (g *directCatalogProbeGuardFixture) MarkCatalogMaintenanceProbeAccepted(jobID string) error {
	if !g.reserved || g.jobID != jobID || g.accepted {
		return ErrDirectHTTPSAPIConfiguration
	}
	g.accepted = true
	return nil
}
func (g *directCatalogProbeGuardFixture) AllowsJobRead(controller, jobID string, now time.Time) bool {
	return g.accepted && controller == g.controller && jobID == g.jobID && !now.IsZero()
}

func TestCatalogMaintenanceDirectAPIAdmitsOnlyOneBoundProbeAndItsRead(t *testing.T) {
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":"printf bounded","timeout_seconds":5}`)
	service, _ := newP046Service(t, &p046FakeRuntime{generation: "catalog-probe-fixture"})
	handler, err := newDirectHTTPSAPIHandlerWithQueueWake(service, lifecycle.NewGate(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	api := handler.(*directHTTPSAPI)
	guard := &directCatalogProbeGuardFixture{
		operationID: strings.Repeat("a", 32), controller: string(controller.ID()), token: strings.Repeat("T", 43),
		key: "catalog-probe-fixture", body: body,
	}
	api.maintenanceHeld = true
	api.maintenanceProbeGuard = guard

	request := func(method, path string, requestBody []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(requestBody))
		req = req.WithContext(context.WithValue(req.Context(), directPrincipalContextKey{}, DirectPrincipal{Controller: controller}))
		if method == http.MethodPost {
			req.Header.Set(maintenanceProbeTokenHeader, guard.token)
			req.Header.Set("Idempotency-Key", guard.key)
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, req)
		return response
	}

	ordinary := request(http.MethodPost, "/v1/jobs", []byte(`{"environment":"linux-dev"}`))
	if ordinary.Code != http.StatusServiceUnavailable || !strings.Contains(ordinary.Body.String(), "maintenance_admission_closed") {
		t.Fatalf("ordinary request during hold status=%d body=%s", ordinary.Code, ordinary.Body.String())
	}
	accepted := request(http.MethodPost, "/v1/jobs", body)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("scoped bounded probe status=%d body=%s", accepted.Code, accepted.Body.String())
	}
	if !guard.accepted || guard.jobID == "" {
		t.Fatalf("scoped probe was not bound and accepted: %+v", guard)
	}
	replay := request(http.MethodPost, "/v1/jobs", body)
	if replay.Code != http.StatusServiceUnavailable {
		t.Fatalf("second probe request status=%d body=%s, want refused", replay.Code, replay.Body.String())
	}
	read := request(http.MethodGet, "/v1/jobs/"+guard.jobID, nil)
	if read.Code != http.StatusOK {
		t.Fatalf("exact accepted job read status=%d body=%s", read.Code, read.Body.String())
	}
	otherRead := request(http.MethodGet, "/v1/jobs/job-ffffffffffffffffffffffffffffffff", nil)
	if otherRead.Code != http.StatusServiceUnavailable {
		t.Fatalf("unscoped job read status=%d body=%s, want refused", otherRead.Code, otherRead.Body.String())
	}
}
