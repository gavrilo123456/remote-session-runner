package domain

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestP007SerializedRequestAndFrameBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		validate func([]byte) error
	}{
		{name: "request", validate: ValidateSerializedRequest},
		{name: "frame", validate: ValidateSerializedFrame},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, size := range []int{0, MaxSerializedRequestBytes - 1, MaxSerializedRequestBytes} {
				raw := bytes.Repeat([]byte{'x'}, size)
				if err := test.validate(raw); err != nil {
					t.Errorf("%d-byte serialized input rejected: %v", size, err)
				}
			}

			raw := bytes.Repeat([]byte{'x'}, MaxSerializedRequestBytes+1)
			if err := test.validate(raw); !errors.Is(err, ErrSerializedInputTooLarge) {
				t.Fatalf("%d-byte serialized input error = %v, want ErrSerializedInputTooLarge", len(raw), err)
			}
		})
	}
}

func TestP007SerializedOversizeIsRejectedWithoutDecoding(t *testing.T) {
	// This deliberately is not JSON: the byte ceiling must be usable before a
	// decoder is invoked, including for a frame with no newline yet received.
	raw := bytes.Repeat([]byte{'{'}, MaxSerializedRequestBytes+1)
	if err := ValidateSerializedRequest(raw); !errors.Is(err, ErrSerializedInputTooLarge) {
		t.Fatalf("oversized raw request error = %v, want ErrSerializedInputTooLarge", err)
	}

	frame := bytes.Repeat([]byte{'{'}, MaxSerializedFrameBytes+1)
	if err := ValidateSerializedFrame(frame); !errors.Is(err, ErrSerializedInputTooLarge) {
		t.Fatalf("oversized raw frame error = %v, want ErrSerializedInputTooLarge", err)
	}
}

func TestP007ScriptUTF8ByteBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		wantErr error
	}{
		{name: "empty script is allowed", script: ""},
		{name: "ASCII below limit", script: strings.Repeat("x", MaxScriptUTF8Bytes-1)},
		{name: "ASCII at limit", script: strings.Repeat("x", MaxScriptUTF8Bytes)},
		{name: "ASCII above limit", script: strings.Repeat("x", MaxScriptUTF8Bytes+1), wantErr: ErrScriptTooLarge},
		{name: "two-byte UTF-8 at limit", script: strings.Repeat("é", MaxScriptUTF8Bytes/2)},
		{name: "two-byte UTF-8 above limit", script: strings.Repeat("é", MaxScriptUTF8Bytes/2+1), wantErr: ErrScriptTooLarge},
		{name: "four-byte UTF-8 at limit", script: strings.Repeat("🧪", MaxScriptUTF8Bytes/4)},
		{name: "four-byte UTF-8 above limit", script: strings.Repeat("🧪", MaxScriptUTF8Bytes/4+1), wantErr: ErrScriptTooLarge},
		{name: "invalid UTF-8", script: string([]byte{0xff}), wantErr: ErrScriptInvalidUTF8},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateScriptUTF8(test.script)
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateScriptUTF8 rejected %d-byte script: %v", len(test.script), err)
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateScriptUTF8 error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
		})
	}
}

func TestP007RejectedInputsDoNotReachAcceptanceEffects(t *testing.T) {
	tests := []struct {
		name   string
		body   []byte
		script string
	}{
		{
			name: "oversized request",
			body: bytes.Repeat([]byte{'x'}, MaxSerializedRequestBytes+1),
		},
		{
			name:   "oversized script",
			body:   []byte(`{"script":"x"}`),
			script: strings.Repeat("x", MaxScriptUTF8Bytes+1),
		},
		{
			name:   "invalid UTF-8 script",
			body:   []byte(`{"script":"x"}`),
			script: string([]byte{0xff}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var effects struct {
				intentRows  int
				events      int
				runtimeRuns int
			}
			accept := func() {
				effects.intentRows++
				effects.events++
				effects.runtimeRuns++
			}

			if err := validateP007ThenAccept(test.body, test.script, accept); err == nil {
				t.Fatal("invalid input was accepted")
			}
			if effects.intentRows != 0 || effects.events != 0 || effects.runtimeRuns != 0 {
				t.Fatalf("rejected input reached acceptance effects: %+v", effects)
			}
		})
	}

	var accepted struct {
		intentRows  int
		events      int
		runtimeRuns int
	}
	err := validateP007ThenAccept([]byte(`{"script":"ok"}`), "ok", func() {
		accepted.intentRows++
		accepted.events++
		accepted.runtimeRuns++
	})
	if err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	if accepted.intentRows != 1 || accepted.events != 1 || accepted.runtimeRuns != 1 {
		t.Fatalf("valid input did not proceed to the acceptance tripwire: %+v", accepted)
	}
}

func validateP007ThenAccept(body []byte, script string, accept func()) error {
	if err := ValidateSerializedRequest(body); err != nil {
		return err
	}
	if err := ValidateScriptUTF8(script); err != nil {
		return err
	}
	accept()
	return nil
}
