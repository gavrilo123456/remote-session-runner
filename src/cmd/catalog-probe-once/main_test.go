package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestCompactPreparedRequestBodyRestoresPreparedDigest(t *testing.T) {
	want := []byte(`{"environment":"sandbox-host-dev","timeout_seconds":5}`)
	digest := sha256.Sum256(want)
	input := json.RawMessage("{\n  \"environment\": \"sandbox-host-dev\",\n  \"timeout_seconds\": 5\n}")
	got, err := compactPreparedRequestBody(input, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatalf("compact prepared body: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("compacted body = %s, want %s", got, want)
	}
}

func TestCompactPreparedRequestBodyRejectsDigestMismatch(t *testing.T) {
	if _, err := compactPreparedRequestBody(json.RawMessage(`{"environment":"sandbox-host-dev"}`), "0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("digest mismatch was accepted")
	}
}

func TestProbeOutcomePrintsEvidenceAndFailsAcceptanceWhenAnyGateFails(t *testing.T) {
	exitCode := 0
	finalSequence := int64(2)
	base := outcome{Phase: "complete", CommandState: "succeeded", ExitCode: &exitCode, FinalEventSequence: &finalSequence,
		OutputComplete: true, TeardownState: "closed"}
	if !probeOutcomeAccepted(base) {
		t.Fatal("complete, zero-exit, complete-output, closed-teardown probe was rejected")
	}
	tests := []struct {
		name   string
		mutate func(*outcome)
	}{
		{"failed job", func(value *outcome) { value.Phase = "failed" }},
		{"lost job", func(value *outcome) { value.Phase = "lost" }},
		{"nonzero exit", func(value *outcome) { code := 3; value.ExitCode = &code }},
		{"missing exit", func(value *outcome) { value.ExitCode = nil }},
		{"incomplete output", func(value *outcome) { value.OutputComplete = false }},
		{"truncated output", func(value *outcome) { value.OutputTruncated = true }},
		{"missing final sequence", func(value *outcome) { value.FinalEventSequence = nil }},
		{"unconfirmed teardown", func(value *outcome) { value.TeardownState = "lost" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			var output bytes.Buffer
			if err := printProbeOutcomeTo(&output, value); !errors.Is(err, errProbeNotAccepted) {
				t.Fatalf("failure result error=%v, want acceptance failure", err)
			}
			if output.Len() == 0 {
				t.Fatal("diagnostic evidence was not emitted")
			}
			var emitted outcome
			if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &emitted); err != nil || !reflect.DeepEqual(emitted, value) {
				t.Fatalf("failure evidence was not emitted exactly once: decoded=%+v err=%v output=%q", emitted, err, output.String())
			}
			if probeOutcomeAccepted(value) {
				t.Fatal("incomplete or unsuccessful command was accepted")
			}
		})
	}
}

func TestDecodeObservedJobResponseRequiresExactTerminalResourceIdentity(t *testing.T) {
	const jobID = "job-705974bc8f474d77898670837be15b61"
	good := response{status: http.StatusOK, body: []byte(`{"resource":{"job_id":"job-705974bc8f474d77898670837be15b61","session_id":"sess-11111111111111111111111111111111","command_id":"cmd-22222222222222222222222222222222","phase":"complete","command_state":"succeeded","exit_code":0,"final_event_sequence":4,"output_complete":true,"output_truncated":false,"teardown_state":"closed"}}`)}
	got, err := decodeObservedJobResponse(good, jobID)
	if err != nil || got.JobID != jobID || !probeOutcomeAccepted(outcome{Phase: got.Phase, CommandState: got.CommandState,
		ExitCode: got.ExitCode, FinalEventSequence: got.FinalEventSequence, OutputComplete: got.OutputComplete,
		OutputTruncated: got.OutputTruncated, TeardownState: got.TeardownState}) {
		t.Fatalf("valid exact status response was rejected: %+v, %v", got, err)
	}
	if _, err := decodeObservedJobResponse(response{status: http.StatusNotFound, body: []byte(`{"code":"job_not_found"}`)}, jobID); err == nil ||
		!strings.Contains(err.Error(), "HTTP 404") || !strings.Contains(err.Error(), "job_not_found") {
		t.Fatalf("missing job did not return bounded status classification: %v", err)
	}
	if _, err := decodeObservedJobResponse(response{status: http.StatusServiceUnavailable, body: []byte(`{"code":"maintenance_admission_closed"}`)}, jobID); err == nil ||
		!strings.Contains(err.Error(), "HTTP 503") || !strings.Contains(err.Error(), "maintenance_admission_closed") {
		t.Fatalf("closed maintenance status route lost its bounded response classification: %v", err)
	}
	if _, err := decodeObservedJobResponse(response{status: http.StatusOK, body: []byte(`{"resource":{"job_id":"job-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)}, jobID); err == nil {
		t.Fatal("status result from a different job was accepted")
	}
}
