package runnerlocal

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/opshealth"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
)

func TestP128MacIngressReadinessSeparatesRemoteRouterOutage(t *testing.T) {
	fixture := testfixture.New(t)
	db, err := store.Open(context.Background(), filepath.Join(fixture.Path(), "state", "p128-health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := store.NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	monitor := newRouterHealthMonitor()
	monitor.update(errors.New("secret-bearing transport detail"), time.Now())
	report := macIngressHealthReport(context.Background(), authority, monitor)
	if report.Readiness != opshealth.StateReady || len(report.Checks) != 2 {
		t.Fatalf("ingress report=%+v; a Router outage must not block durable ingress", report)
	}
	if report.Checks[1].State != opshealth.StateDegraded || report.Checks[1].Reason != "remote_transport_unavailable" {
		t.Fatalf("Router status=%+v", report.Checks[1])
	}
	if report.Checks[1].Reason == "secret-bearing transport detail" {
		t.Fatal("health report exposed raw transport error")
	}
}

func TestP128RouterHealthBecomesDegradedWhenProbeGetsStale(t *testing.T) {
	monitor := newRouterHealthMonitor()
	now := time.Now().UTC()
	monitor.update(nil, now)
	if got := monitor.current(now.Add(routerHealthMaxAge + time.Second)); got.state != opshealth.StateDegraded || got.reason != "remote_health_stale" {
		t.Fatalf("stale Router snapshot=%+v", got)
	}
}

func TestP128DoctorClassifiesDatabaseStartupFailureWithoutErrorText(t *testing.T) {
	secretBearingCause := errors.New("private database path and credential marker")
	report := macDoctorStartupFailureReport(errors.Join(errMacDatabaseNotReady, secretBearingCause))
	if report.Readiness != opshealth.StateNotReady || len(report.Checks) != 1 || report.Checks[0].Component != "sqlite_writes" || report.Checks[0].Reason != "database_migration_or_write_failed" {
		t.Fatalf("Mac doctor startup failure report=%+v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secretBearingCause.Error()) {
		t.Fatal("Mac doctor report exposed raw database error detail")
	}
}
