package localapi

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
