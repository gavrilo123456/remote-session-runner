// Package opshealth provides safe, dependency-aware health reports for Runner
// services and the HTTP liveness/readiness endpoints.
package opshealth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
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
	Component  string    `json:"component"`
	Liveness   string    `json:"liveness"`
	Readiness  State     `json:"readiness"`
	ObservedAt time.Time `json:"observed_at"`
	Checks     []Check   `json:"checks"`
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
	return Report{Component: component, Liveness: "not_checked", Readiness: readiness, ObservedAt: observedAt, Checks: append([]Check{}, checks...)}
}

// ServeHealth handles GET /health/live and GET /health/ready. It returns true
// only for those paths, allowing the caller to keep health routes separate
// from its application API and to share the same report with doctor.
func ServeHealth(component string, w http.ResponseWriter, r *http.Request, report func(context.Context) Report) bool {
	if r == nil || (r.URL.Path != "/health/live" && r.URL.Path != "/health/ready") {
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
