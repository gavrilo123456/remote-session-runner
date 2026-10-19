package runnerlocald

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/opshealth"
)

func TestMaintenanceAdmissionLeavesHealthReadableButRejectsLocaldWrites(t *testing.T) {
	server := &PrivateServer{
		maintenanceAdmissionClosed: true,
		requestGate:                lifecycle.NewGate(),
		healthReport: func(context.Context) opshealth.Report {
			return opshealth.NewReport("mac_local_executor", time.Now(), opshealth.Check{Component: "fixture", State: opshealth.StateReady, RequiredForReadiness: true})
		},
	}
	post := httptest.NewRequest(http.MethodPost, "/internal/v1/accept-intent", strings.NewReader(`{}`))
	postResponse := httptest.NewRecorder()
	server.serveHTTP(postResponse, post)
	if postResponse.Code != http.StatusServiceUnavailable || !strings.Contains(postResponse.Body.String(), "maintenance_admission_closed") {
		t.Fatalf("locald mutation status=%d body=%s", postResponse.Code, postResponse.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	getResponse := httptest.NewRecorder()
	server.serveHTTP(getResponse, get)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("read-only health status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}
}
