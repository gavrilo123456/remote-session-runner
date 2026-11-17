package localapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/config"
	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/laneinstall"
	"remote-session-runner/src/internal/lifecycle"
	"remote-session-runner/src/internal/opshealth"
	"remote-session-runner/src/internal/store"
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

type maintenanceProbeCountingBody struct {
	io.Reader
	reads int
}

func (body *maintenanceProbeCountingBody) Read(value []byte) (int, error) {
	body.reads++
	return body.Reader.Read(value)
}

func TestMaintenanceProbeTypedNilPreservesAdmissionBehavior(t *testing.T) {
	for _, setter := range []bool{false, true} {
		for _, held := range []bool{false, true} {
			t.Run(fmt.Sprintf("setter=%t/held=%t", setter, held), func(t *testing.T) {
				root := t.TempDir()
				state := filepath.Join(root, "state")
				db, err := store.Open(context.Background(), filepath.Join(state, "authority.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				authority, err := store.NewAuthorityStore(db)
				if err != nil {
					t.Fatal(err)
				}
				var absent *laneinstall.CatalogMaintenanceProbeGuard
				options := ServerOptions{
					Authority: authority, Owner: p063Owner(t), SocketPath: filepath.Join(state, "local.sock"),
					MaintenanceAdmissionClosed: held, MaintenanceProbeGuard: absent,
					HealthReport: func(context.Context) opshealth.Report {
						return opshealth.NewReport("mac_ingress", time.Now(), opshealth.Check{Component: "fixture", State: opshealth.StateReady, RequiredForReadiness: true})
					},
				}
				if setter {
					plan, err := laneinstall.BuildCatalogPlan("mac-control-plane", config.ReleaseLaneDev)
					if err != nil {
						t.Fatal(err)
					}
					options.MaintenanceProbeGuard, err = laneinstall.NewCatalogMaintenanceProbeGuardForTransport(plan, strings.Repeat("a", 32), laneinstall.CatalogProbeMacLocal)
					if err != nil {
						t.Fatal(err)
					}
				}
				server, err := NewServer(options)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(server.cancelRequests)
				dispatches := 0
				if setter {
					if server.maintenanceProbeGuard == nil {
						t.Fatal("constructor dropped the installed guard")
					}
					server.SetMaintenanceProbe(absent, func(context.Context, string, string) error {
						dispatches++
						return nil
					})
				}
				if server.maintenanceProbeGuard != nil {
					t.Fatal("typed-nil guard did not clear the probe scope")
				}
				body := &maintenanceProbeCountingBody{Reader: strings.NewReader(`{}`)}
				request := httptest.NewRequest(http.MethodPost, "/v1/jobs", body)
				request.Header.Set("Idempotency-Key", "typed-nil-probe")
				response := httptest.NewRecorder()
				server.serveHTTP(response, request)
				wantStatus := http.StatusUnprocessableEntity
				if held {
					wantStatus = http.StatusServiceUnavailable
				}
				if response.Code != wantStatus || dispatches != 0 {
					t.Fatalf("nil scope: status=%d dispatches=%d body=%s", response.Code, dispatches, response.Body.String())
				}
				if held && (body.reads != 0 || !strings.Contains(response.Body.String(), "maintenance_admission_closed")) {
					t.Fatalf("held nil scope read the request or changed its refusal: reads=%d body=%s", body.reads, response.Body.String())
				}
				if !held && body.reads == 0 {
					t.Fatal("unheld request did not reach ordinary validation")
				}
				ready := httptest.NewRecorder()
				server.serveHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
				if ready.Code != http.StatusOK {
					t.Fatalf("nil scope changed read-only readiness: status=%d body=%s", ready.Code, ready.Body.String())
				}
			})
		}
	}
}
