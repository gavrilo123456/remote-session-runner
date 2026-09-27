package runnerlocald

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/execution"
)

func TestP104IntentAcceptanceFailureUsesSafeStableErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "session not ready", err: fmt.Errorf("detail: %w", execution.ErrSessionNotReady), code: "session_not_ready"},
		{name: "unknown internal error", err: errors.New("secret path /private/value"), code: "intent_acceptance_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, message := intentAcceptanceFailure(test.err)
			if code != test.code || strings.TrimSpace(message) == "" {
				t.Fatalf("acceptance failure=(%q,%q), want code %q and a safe message", code, message, test.code)
			}
			if strings.Contains(message, "secret path") || strings.Contains(message, "/private/value") {
				t.Fatalf("acceptance failure leaked internal detail: %q", message)
			}
		})
	}
}

func TestP104DecodeIntentPayloadAcceptsCanonicalExponentIntegers(t *testing.T) {
	payload, err := decodeIntentPayload([]byte(`{"timeout_seconds":3e1,"command_timeout_seconds":1.8e3,"idle_timeout_seconds":1.8e3,"session_max_lifetime_seconds":1.44e4,"output_bytes_per_command":1e8}`))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Timeout != 30*time.Second || payload.RequestedLimits.CommandTimeout != 30*time.Minute || payload.RequestedLimits.IdleTimeout != 30*time.Minute || payload.RequestedLimits.SessionMaxLifetime != 4*time.Hour || payload.RequestedLimits.OutputBytesPerCommand != 100_000_000 {
		t.Fatalf("decoded canonical payload=%+v", payload)
	}
}

func TestP104DecodeIntentPayloadRejectsFractionalAndOutOfRangeIntegers(t *testing.T) {
	for _, raw := range []string{
		`{"timeout_seconds":1.5}`,
		`{"timeout_seconds":1e100}`,
		`{"output_bytes_per_command":1.25}`,
	} {
		if _, err := decodeIntentPayload([]byte(raw)); err == nil {
			t.Errorf("decodeIntentPayload(%s) accepted a non-integer or out-of-range value", raw)
		}
	}
}
