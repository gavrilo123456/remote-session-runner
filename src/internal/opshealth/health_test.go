package opshealth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestP128OptionalRouterDegradationKeepsIngressReady(t *testing.T) {
	report := NewReport("mac_ingress", time.Unix(1, 0),
		Check{Component: "sqlite_writes", State: StateReady, RequiredForReadiness: true},
		Check{Component: "remote_router", State: StateDegraded, Reason: "remote_transport_unavailable"},
	)
	if report.Readiness != StateReady || len(report.Checks) != 2 || report.Checks[1].State != StateDegraded {
		t.Fatalf("report = %+v; remote outage must degrade Router without taking down ingress", report)
	}
}

func TestP128RequiredFailureMakesReadinessUnavailable(t *testing.T) {
	report := NewReport("runnerd", time.Unix(1, 0),
		Check{Component: "sqlite_writes", State: StateReady, RequiredForReadiness: true},
		Check{Component: "linux_host_profile", State: StateNotReady, Reason: "host_profile_not_ready", RequiredForReadiness: true},
	)
	if report.Readiness != StateNotReady {
		t.Fatalf("readiness = %q, want not_ready", report.Readiness)
	}
}

func TestP128HealthEndpointsSeparateLivenessAndReadiness(t *testing.T) {
	ready := NewReport("runnerd", time.Unix(1, 0), Check{Component: "sqlite_writes", State: StateNotReady, Reason: "database_write_failed", RequiredForReadiness: true})
	for _, test := range []struct {
		path       string
		wantStatus int
		wantBody   string
	}{
		{path: "/health/live", wantStatus: http.StatusOK, wantBody: `"liveness":"live"`},
		{path: "/health/ready", wantStatus: http.StatusServiceUnavailable, wantBody: `"readiness":"not_ready"`},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			if !ServeHealth("runnerd", response, request, func(context.Context) Report { return ready }) {
				t.Fatal("health path was not handled")
			}
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			var decoded map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("health response is not JSON: %v", err)
			}
			if !strings.Contains(response.Body.String(), test.wantBody) {
				t.Fatalf("body %s does not contain %s", response.Body.String(), test.wantBody)
			}
		})
	}
	request := httptest.NewRequest(http.MethodPost, "/health/ready", nil)
	response := httptest.NewRecorder()
	if !ServeHealth("runnerd", response, request, func(context.Context) Report { return ready }) || response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST readiness status = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/v1/ordinary", nil)
	if ServeHealth("runnerd", httptest.NewRecorder(), request, nil) {
		t.Fatal("ordinary API path was handled as health")
	}
}
