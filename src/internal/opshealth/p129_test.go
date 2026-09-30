package opshealth

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestP129ConcurrentRecorderCounters(t *testing.T) {
	recorder := NewRecorder()
	const workers, increments = 8, 250
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range increments {
				recorder.RecordCleanupFailure()
			}
		}()
	}
	wait.Wait()
	var metrics Metrics
	recorder.AddTo(&metrics)
	if metrics.CleanupFailuresTotal != workers*increments {
		t.Fatalf("recorded cleanup total = %d, want %d", metrics.CleanupFailuresTotal, workers*increments)
	}
}

func TestP129MetricsEndpointReturnsOnlyBoundedMetrics(t *testing.T) {
	want := Metrics{ActiveSessionSlots: 2, ActiveCommandSlots: 1, QueuedCommands: 3, QueuedIntents: 4, DispatchAttemptsTotal: 5, ReconciliationAgeSeconds: 6, EventLagEvents: 7, EventGapsTotal: 8, OutputTruncationsTotal: 9, StorageErrorsTotal: 10, CleanupFailuresTotal: 11, MailboxBacklog: 12}
	report := NewReport("runnerd", time.Unix(1, 0), Check{Component: "sqlite_writes", State: StateReady, RequiredForReadiness: true})
	report.Metrics = &want
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if !ServeHealth("runnerd", response, request, func(context.Context) Report { return report }) {
		t.Fatal("/metrics was not handled")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("/metrics status=%d body=%s", response.Code, response.Body.String())
	}
	var got Metrics
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode /metrics JSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("/metrics = %+v, want %+v", got, want)
	}
	for _, forbidden := range []string{"command_id", "session_id", "controller", "script", "secret", "password", "private_key"} {
		if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
			t.Fatalf("/metrics exposed forbidden label %q: %s", forbidden, response.Body.String())
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control=%q", response.Header().Get("Cache-Control"))
	}
}

func TestP129DoctorOutputIncludesTheSameMetricsObject(t *testing.T) {
	want := Metrics{QueuedIntents: 7, StorageErrorsTotal: 2}
	report := NewReport("mac_ingress", time.Unix(1, 0), Check{Component: "sqlite_writes", State: StateReady, RequiredForReadiness: true})
	report.Metrics = &want
	var output bytes.Buffer
	if err := WriteDoctor(&output, report); err != nil {
		t.Fatal(err)
	}
	var got Report
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("decode doctor report: %v", err)
	}
	if got.Metrics == nil || !reflect.DeepEqual(*got.Metrics, want) {
		t.Fatalf("doctor metrics=%+v, want %+v", got.Metrics, want)
	}
}

func TestP129ThresholdLogsOnlyEntryAndClearTransitions(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelInfo}))
	monitor := NewThresholdMonitor()
	metrics := Metrics{QueuedCommands: 16}
	monitor.LogThresholds(logger, "runnerd", metrics)
	monitor.LogThresholds(logger, "runnerd", metrics)
	monitor.LogThresholds(logger, "runnerd", Metrics{})
	log := output.String()
	if got := strings.Count(log, "runner operational metric threshold exceeded"); got != 1 {
		t.Fatalf("threshold-entry log count=%d log=%s", got, log)
	}
	if got := strings.Count(log, "runner operational metric threshold cleared"); got != 1 {
		t.Fatalf("threshold-clear log count=%d log=%s", got, log)
	}
	if strings.Contains(log, "session_id") || strings.Contains(log, "secret") {
		t.Fatalf("threshold log included sensitive data: %s", log)
	}
}
