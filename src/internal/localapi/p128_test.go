package localapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/opshealth"
)

func TestP128MacHealthKeepsDurableIngressReadyWhenRouterIsDegraded(t *testing.T) {
	h := newP095Harness(t)
	h.server.healthReport = func(context.Context) opshealth.Report {
		return opshealth.NewReport("mac_ingress", time.Unix(1, 0),
			opshealth.Check{Component: "sqlite_writes", State: opshealth.StateReady, RequiredForReadiness: true},
			opshealth.Check{Component: "remote_router", State: opshealth.StateDegraded, Reason: "remote_transport_unavailable", Details: map[string]int64{"pending_intents": 2, "uncertain_intents": 1}},
		)
	}
	for _, path := range []string{"/health/live", "/health/ready"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		h.server.serveHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", path, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s cache control=%q", path, response.Header().Get("Cache-Control"))
		}
		if !strings.Contains(response.Body.String(), "mac_ingress") {
			t.Fatalf("GET %s omitted component status: %s", path, response.Body.String())
		}
		if path == "/health/ready" && !strings.Contains(response.Body.String(), "remote_transport_unavailable") {
			t.Fatalf("readiness omitted degraded Router status: %s", response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	response := httptest.NewRecorder()
	h.server.serveHTTP(response, request)
	var report opshealth.Report
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Readiness != opshealth.StateReady || len(report.Checks) != 2 || report.Checks[1].State != opshealth.StateDegraded || report.Checks[1].Details["pending_intents"] != 2 || report.Checks[1].Details["uncertain_intents"] != 1 {
		t.Fatalf("Mac health report=%+v; remote failure must remain separate from durable ingress readiness", report)
	}
}

func TestP128MacHealthReadinessReturnsUnavailableForRequiredDatabaseFailure(t *testing.T) {
	h := newP095Harness(t)
	h.server.healthReport = func(context.Context) opshealth.Report {
		return opshealth.NewReport("mac_ingress", time.Unix(1, 0), opshealth.Check{Component: "sqlite_writes", State: opshealth.StateNotReady, Reason: "database_write_failed", RequiredForReadiness: true})
	}
	request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	response := httptest.NewRecorder()
	h.server.serveHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "database_write_failed") {
		t.Fatalf("readiness status=%d body=%s", response.Code, response.Body.String())
	}
}
