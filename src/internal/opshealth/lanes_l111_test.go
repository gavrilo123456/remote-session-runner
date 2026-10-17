package opshealth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/buildinfo"
)

func TestLanesL111V3AttestationIsSafeAndV2HealthShapeStaysUnchanged(t *testing.T) {
	priorRevision := buildinfo.SourceRevision
	buildinfo.SourceRevision = strings.Repeat("a", 40)
	t.Cleanup(func() { buildinfo.SourceRevision = priorRevision })

	attestation := &ReleaseAttestation{
		ReleaseLane: "dev", SourceRevision: buildinfo.Revision(),
		ArtifactDigest:      "sha256:" + strings.Repeat("b", 64),
		ConfigurationDigest: "sha256:" + strings.Repeat("c", 64),
		SchemaVersion:       3, AdmissionStatus: AdmissionOpen,
	}
	observedAt := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	report := Report{
		BuildRevision: "callback-is-not-authoritative", Readiness: StateReady, ObservedAt: observedAt,
		Checks:             []Check{{Component: "private", State: StateDegraded, Reason: "/secret/path"}},
		ReleaseAttestation: attestation,
	}
	for _, path := range []string{"/v3/health/ready", "/v3/status"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, path, nil)
			if !ServeHealth("mac_ingress", response, request, func(context.Context) Report { return report }) {
				t.Fatal("V3 route was not handled")
			}
			if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache-control=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("decode V3 response: %v", err)
			}
			wantKeys := []string{"api_version", "component", "liveness", "readiness", "observed_at", "release_attestation"}
			gotKeys := make([]string, 0, len(decoded))
			for key := range decoded {
				gotKeys = append(gotKeys, key)
			}
			// JSON object ordering is irrelevant; compare key membership.
			for _, key := range wantKeys {
				if _, ok := decoded[key]; !ok {
					t.Errorf("V3 response missing %q: %s", key, response.Body.String())
				}
			}
			if len(gotKeys) != len(wantKeys) {
				t.Fatalf("V3 response fields=%v, want only %v", gotKeys, wantKeys)
			}
			var gotAttestation ReleaseAttestation
			if err := json.Unmarshal(decoded["release_attestation"], &gotAttestation); err != nil || gotAttestation != *attestation {
				t.Fatalf("release attestation=%+v err=%v, want %+v", gotAttestation, err, *attestation)
			}
			if strings.Contains(response.Body.String(), "/secret/path") || strings.Contains(response.Body.String(), "callback-is-not-authoritative") {
				t.Fatalf("V3 response leaked a diagnostic or callback build revision: %s", response.Body.String())
			}
		})
	}

	// The legacy endpoint does not serialize the optional V3 value.
	legacy := httptest.NewRecorder()
	if !ServeHealth("mac_ingress", legacy, httptest.NewRequest(http.MethodGet, "/health/ready", nil), func(context.Context) Report { return report }) {
		t.Fatal("V2 readiness route was not handled")
	}
	var legacyFields map[string]json.RawMessage
	if err := json.Unmarshal(legacy.Body.Bytes(), &legacyFields); err != nil {
		t.Fatal(err)
	}
	if _, found := legacyFields["release_attestation"]; found {
		t.Fatalf("V2 health gained V3 attestation: %s", legacy.Body.String())
	}

	invalid := []struct {
		name   string
		change func(*ReleaseAttestation)
	}{
		{name: "unknown lane", change: func(value *ReleaseAttestation) { value.ReleaseLane = "../../prd" }},
		{name: "source revision differs from serving build", change: func(value *ReleaseAttestation) { value.SourceRevision = strings.Repeat("d", 40) }},
		{name: "artifact digest is a path", change: func(value *ReleaseAttestation) { value.ArtifactDigest = "/private/key" }},
		{name: "configuration digest is malformed", change: func(value *ReleaseAttestation) { value.ConfigurationDigest = "sha256:ABC" }},
		{name: "wrong schema version", change: func(value *ReleaseAttestation) { value.SchemaVersion = 2 }},
		{name: "unknown admission status", change: func(value *ReleaseAttestation) { value.AdmissionStatus = "open;secret" }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			value := *attestation
			test.change(&value)
			response := httptest.NewRecorder()
			if !ServeHealth("mac_ingress", response, httptest.NewRequest(http.MethodGet, "/v3/status", nil), func(context.Context) Report {
				copy := report
				copy.ReleaseAttestation = &value
				return copy
			}) {
				t.Fatal("V3 status route was not handled")
			}
			if response.Code != http.StatusServiceUnavailable || response.Body.String() != "{\"error\":\"v3_attestation_unavailable\"}\n" {
				t.Fatalf("unsafe attestation response status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	notReady := report
	notReady.Readiness = StateNotReady
	status := httptest.NewRecorder()
	if !ServeHealth("runnerd", status, httptest.NewRequest(http.MethodGet, "/v3/status", nil), func(context.Context) Report { return notReady }) || status.Code != http.StatusOK {
		t.Fatalf("read-only status must remain available while not ready: status=%d body=%s", status.Code, status.Body.String())
	}
	ready := httptest.NewRecorder()
	if !ServeHealth("runnerd", ready, httptest.NewRequest(http.MethodGet, "/v3/health/ready", nil), func(context.Context) Report { return notReady }) || ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("V3 readiness status=%d, want service unavailable", ready.Code)
	}
	invalidReadiness := httptest.NewRecorder()
	badState := report
	badState.Readiness = State("private diagnostic")
	if !ServeHealth("runnerd", invalidReadiness, httptest.NewRequest(http.MethodGet, "/v3/status", nil), func(context.Context) Report { return badState }) ||
		invalidReadiness.Code != http.StatusServiceUnavailable || invalidReadiness.Body.String() != "{\"error\":\"v3_attestation_unavailable\"}\n" {
		t.Fatalf("invalid readiness escaped bounded V3 status: status=%d body=%s", invalidReadiness.Code, invalidReadiness.Body.String())
	}
}
