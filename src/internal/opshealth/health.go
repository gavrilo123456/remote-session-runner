// Package opshealth provides safe, dependency-aware health reports for Runner
// services and the HTTP liveness/readiness endpoints.
package opshealth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"remote-session-runner/src/internal/buildinfo"
)

type State string

const (
	StateReady    State = "ready"
	StateDegraded State = "degraded"
	StateNotReady State = "not_ready"
)

// Check reports one dependency or operational component. Reason is a stable,
// secret-free code; raw errors and credential material never belong here.
type Check struct {
	Component            string           `json:"component"`
	State                State            `json:"state"`
	Reason               string           `json:"reason,omitempty"`
	RequiredForReadiness bool             `json:"required_for_readiness"`
	Details              map[string]int64 `json:"details,omitempty"`
}

// Report is shared by doctor output and /health/ready responses. A degraded
// optional component, such as the Mac remote Router, does not take down the
// readiness of durable local ingress.
type Report struct {
	Component     string    `json:"component"`
	BuildRevision string    `json:"build_revision"`
	Liveness      string    `json:"liveness"`
	Readiness     State     `json:"readiness"`
	ObservedAt    time.Time `json:"observed_at"`
	Checks        []Check   `json:"checks"`
	Metrics       *Metrics  `json:"metrics,omitempty"`
}

// Metrics contains bounded, low-cardinality operational measurements. It has
// no resource IDs, principals, paths, scripts, output, or credential values.
type Metrics struct {
	ActiveSessionSlots       int64 `json:"active_session_slots"`
	ActiveCommandSlots       int64 `json:"active_command_slots"`
	QueuedCommands           int64 `json:"queued_commands"`
	QueuedIntents            int64 `json:"queued_intents"`
	DispatchAttemptsTotal    int64 `json:"dispatch_attempts_total"`
	ReconciliationAgeSeconds int64 `json:"reconciliation_age_seconds"`
	EventLagEvents           int64 `json:"event_lag_events"`
	EventGapsTotal           int64 `json:"event_gaps_total"`
	OutputTruncationsTotal   int64 `json:"output_truncations_total"`
	StorageErrorsTotal       int64 `json:"storage_errors_total"`
	CleanupFailuresTotal     int64 `json:"cleanup_failures_total"`
	MailboxBacklog           int64 `json:"mailbox_backlog"`
	// MailboxBacklogByInbox is keyed only by configured safe inbox IDs. It
	// contains durable accepted exchanges plus safely published ready markers
	// for that inbox, while MailboxBacklog remains the aggregate gauge.
	MailboxBacklogByInbox map[string]int64 `json:"mailbox_backlog_by_inbox,omitempty"`
}

// Recorder holds supplementary process-local cleanup failures. SQLite errors
// are counted by AuthorityStore; durable gauges and event counts are queried.
type Recorder struct {
	cleanupFailures atomic.Int64
}

func NewRecorder() *Recorder { return &Recorder{} }

func (r *Recorder) RecordCleanupFailure() {
	if r != nil {
		r.cleanupFailures.Add(1)
	}
}

func (r *Recorder) AddTo(metrics *Metrics) {
	if r == nil || metrics == nil {
		return
	}
	metrics.CleanupFailuresTotal += r.cleanupFailures.Load()
}

// Thresholds are intentionally fixed, bounded PoC defaults. Counters that
// naturally grow for every request (such as dispatch attempts) are reported
// but are not warned on by their absolute total.
var operationalThresholds = map[string]int64{
	"active_session_slots":       16,
	"active_command_slots":       4,
	"queued_commands":            16,
	"queued_intents":             32,
	"reconciliation_age_seconds": 300,
	"event_lag_events":           32,
	"event_gaps_total":           1,
	"output_truncations_total":   1,
	"storage_errors_total":       1,
	"cleanup_failures_total":     1,
	"mailbox_backlog":            32,
}

type thresholdState struct {
	Metric    string
	Value     int64
	Threshold int64
	Exceeded  bool
}

// ThresholdMonitor logs only threshold crossings and clearances, preventing
// periodic health polling from repeating the same warning indefinitely.
type ThresholdMonitor struct {
	mu       sync.Mutex
	exceeded map[string]bool
}

func NewThresholdMonitor() *ThresholdMonitor {
	return &ThresholdMonitor{exceeded: make(map[string]bool)}
}

func (m *ThresholdMonitor) Observe(metrics Metrics) []thresholdState {
	if m == nil {
		return nil
	}
	values := metrics.values()
	names := make([]string, 0, len(operationalThresholds))
	for name := range operationalThresholds {
		names = append(names, name)
	}
	sort.Strings(names)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.exceeded == nil {
		m.exceeded = make(map[string]bool)
	}
	var transitions []thresholdState
	for _, name := range names {
		threshold := operationalThresholds[name]
		value := values[name]
		nowExceeded := value >= threshold
		wasExceeded := m.exceeded[name]
		if nowExceeded != wasExceeded {
			transitions = append(transitions, thresholdState{Metric: name, Value: value, Threshold: threshold, Exceeded: nowExceeded})
			m.exceeded[name] = nowExceeded
		}
	}
	return transitions
}

// LogThresholds emits safe structured warning/info records when values cross
// their documented default thresholds.
func (m *ThresholdMonitor) LogThresholds(logger *slog.Logger, component string, metrics Metrics) {
	if logger == nil {
		logger = slog.Default()
	}
	for _, transition := range m.Observe(metrics) {
		level, message := slog.LevelInfo, "runner operational metric threshold cleared"
		if transition.Exceeded {
			level, message = slog.LevelWarn, "runner operational metric threshold exceeded"
		}
		logger.Log(context.Background(), level, message,
			"component", component,
			"metric", transition.Metric,
			"value", transition.Value,
			"threshold", transition.Threshold,
		)
	}
}

func (m Metrics) values() map[string]int64 {
	return map[string]int64{
		"active_session_slots":       m.ActiveSessionSlots,
		"active_command_slots":       m.ActiveCommandSlots,
		"queued_commands":            m.QueuedCommands,
		"queued_intents":             m.QueuedIntents,
		"reconciliation_age_seconds": m.ReconciliationAgeSeconds,
		"event_lag_events":           m.EventLagEvents,
		"event_gaps_total":           m.EventGapsTotal,
		"output_truncations_total":   m.OutputTruncationsTotal,
		"storage_errors_total":       m.StorageErrorsTotal,
		"cleanup_failures_total":     m.CleanupFailuresTotal,
		"mailbox_backlog":            m.MailboxBacklog,
	}
}

func NewReport(component string, observedAt time.Time, checks ...Check) Report {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	} else {
		observedAt = observedAt.UTC()
	}
	readiness := StateReady
	for _, check := range checks {
		if !check.RequiredForReadiness {
			continue
		}
		if check.State != StateReady {
			readiness = StateNotReady
			break
		}
	}
	return Report{Component: component, BuildRevision: buildinfo.Revision(), Liveness: "not_checked", Readiness: readiness, ObservedAt: observedAt, Checks: append([]Check{}, checks...)}
}

// AddMetrics attaches a bounded snapshot and emits only threshold transitions.
func AddMetrics(report Report, metrics Metrics, recorder *Recorder, monitor *ThresholdMonitor, logger *slog.Logger) Report {
	if recorder != nil {
		recorder.AddTo(&metrics)
	}
	report.Metrics = &metrics
	if monitor != nil {
		monitor.LogThresholds(logger, report.Component, metrics)
	}
	return report
}

// ServeHealth handles GET /health/live and GET /health/ready. It returns true
// only for those paths, allowing the caller to keep health routes separate
// from its application API and to share the same report with doctor.
func ServeHealth(component string, w http.ResponseWriter, r *http.Request, report func(context.Context) Report) bool {
	if r == nil || (r.URL.Path != "/health/live" && r.URL.Path != "/health/ready" && r.URL.Path != "/metrics") {
		return false
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/health/live" {
		_ = json.NewEncoder(w).Encode(struct {
			Component string `json:"component"`
			Liveness  string `json:"liveness"`
		}{Component: component, Liveness: "live"})
		return true
	}
	current := Report{Readiness: StateNotReady}
	if report != nil {
		current = report(r.Context())
	}
	if r.URL.Path == "/metrics" {
		if current.Metrics == nil {
			http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
			return true
		}
		_ = json.NewEncoder(w).Encode(current.Metrics)
		return true
	}
	// Readiness is served by the process whose source revision is being
	// attested. Do not trust a value supplied by a report callback.
	current.BuildRevision = buildinfo.Revision()
	current.Liveness = "live"
	if current.Readiness != StateReady {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(current)
	return true
}

// Middleware serves the health routes before an application handler. Use it
// around the direct HTTPS handler so health remains behind mandatory mTLS.
func Middleware(component string, report func(context.Context) Report, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ServeHealth(component, w, r, report) {
			return
		}
		if next == nil {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WriteDoctor emits the same bounded report format as the readiness endpoint.
func WriteDoctor(w io.Writer, report Report) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
