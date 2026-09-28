package runnerlocald

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"remote-session-runner/src/internal/opshealth"
)

func TestP128MacLocalExecutorReadinessRequiresWritableStoreAndProfile(t *testing.T) {
	authority, service := newP060Service(t)
	server, err := NewPrivateServer(PrivateServerOptions{Authority: authority, Service: service, SocketPath: p060SocketPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	response := httptest.NewRecorder()
	server.serveHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("readiness status=%d body=%s", response.Code, response.Body.String())
	}
	var report opshealth.Report
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Component != "mac_local_executor" || report.Readiness != opshealth.StateReady || len(report.Checks) != 2 {
		t.Fatalf("local executor report=%+v", report)
	}
}
