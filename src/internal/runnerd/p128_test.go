package runnerd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"remote-session-runner/src/internal/opshealth"
)

func TestP128RunnerdReadinessReportsDatabaseAndRequiredTLSFailure(t *testing.T) {
	service, _ := newP046Service(t, &p046FakeRuntime{generation: "p128"})
	server, err := NewPrivateServer(PrivateServerOptions{
		Service: service, SocketPath: filepath.Join(p046SocketParent(t), "p128-runnerd.sock"),
		HealthReport: func(context.Context) opshealth.Report {
			return opshealth.NewReport("linux_runnerd", time.Unix(1, 0),
				opshealth.Check{Component: "sqlite_writes", State: opshealth.StateReady, RequiredForReadiness: true},
				opshealth.Check{Component: "linux_host_profile", State: opshealth.StateReady, RequiredForReadiness: true},
				opshealth.Check{Component: "direct_mtls", State: opshealth.StateNotReady, Reason: "mtls_configuration_not_ready", RequiredForReadiness: true},
			)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	response := httptest.NewRecorder()
	server.serveHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status=%d body=%s", response.Code, response.Body.String())
	}
	var report opshealth.Report
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Component != "linux_runnerd" || report.Checks[2].Reason != "mtls_configuration_not_ready" {
		t.Fatalf("runnerd health report=%+v", report)
	}
}
