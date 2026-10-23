package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
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
