package localapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/laneinstall"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/opshealth"
)

func TestMaintenanceAdmissionKeepsHealthReadableAndBlocksLocalMutations(t *testing.T) {
	server := &Server{
		requestGate: lifecycle.NewGate(), maintenanceAdmissionClosed: true,
		healthReport: func(context.Context) opshealth.Report {
			return opshealth.NewReport("mac_ingress", time.Now(), opshealth.Check{Component: "fixture", State: opshealth.StateReady, RequiredForReadiness: true})
		},
	}
	post := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{}`))
	postResponse := httptest.NewRecorder()
	server.serveHTTP(postResponse, post)
	if postResponse.Code != http.StatusServiceUnavailable || !strings.Contains(postResponse.Body.String(), "maintenance_admission_closed") {
		t.Fatalf("local mutation status=%d body=%s", postResponse.Code, postResponse.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	getResponse := httptest.NewRecorder()
	server.serveHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("read-only health status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}
}

func TestMacLocalMaintenanceProbeBodyIsFixedAndLaneBound(t *testing.T) {
	plan, err := laneinstall.BuildCatalogPlan("mac-control-plane", config.ReleaseLaneDev)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := laneinstall.NewCatalogMaintenanceProbeGuardForTransport(plan, strings.Repeat("a", 32), laneinstall.CatalogProbeMacLocal)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{maintenanceProbeGuard: guard, owner: owner}
	body, err := laneinstall.CatalogMaintenanceProbeRequestBodyForTransport(plan, laneinstall.CatalogProbeMacLocal)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.validateMacLocalMaintenanceProbe("catalog-probe-fixed-key", body); err != nil {
		t.Fatalf("fixed Mac-local probe body rejected: %v", err)
	}
	for _, mutated := range []string{
		strings.Replace(string(body), "catalog release maintenance probe", "arbitrary", 1),
		strings.Replace(string(body), `"timeout_seconds":5`, `"timeout_seconds":10`, 1),
		strings.Replace(string(body), `"mode":"empty"`, `"mode":"path"`, 1),
	} {
		if err := server.validateMacLocalMaintenanceProbe("catalog-probe-fixed-key", []byte(mutated)); err == nil {
			t.Fatalf("unsafe Mac-local probe body accepted: %s", mutated)
		}
	}
}
