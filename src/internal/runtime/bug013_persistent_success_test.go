package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestBUG013SlowOutputDeliveryDoesNotFakeOutputBoundary models a normal
// status write that reaches the durable-event callback slowly. The script has
// already returned zero and printed its post-command marker; callback latency
// alone must not convert that proven shell completion into command_lost.
func TestBUG013SlowOutputDeliveryDoesNotFakeOutputBoundary(t *testing.T) {
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{
		SessionID:  "session-bug013-slow-delivery",
		Generation: "generation-bug013-slow-delivery",
		Workspace:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := shell.Close(); err != nil {
			t.Errorf("close persistent shell: %v", err)
		}
	}()

	var callbacks atomic.Int32
	started := time.Now()
	result, err := shell.RunScriptWithOutput(context.Background(), "command-bug013-slow-delivery", []byte("set -euo pipefail\nprintf '%s\\n' 'Everything up-to-date' >&2\nprintf '%s\\n' 'GIT_PUSH_DRY_RUN_WITH_EXPLICIT_KEY_OK'\n"), func(OutputChunk) error {
		callbacks.Add(1)
		// Longer than the physical one-second EOF grace. This represents a
		// temporarily slow durable event append, not an output writer that
		// remains open after the command returns.
		time.Sleep(1200 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatalf("successful status command error = %v", err)
	}
	if elapsed := time.Since(started); elapsed < time.Second {
		t.Fatalf("output callback was not exercised long enough: %s", elapsed)
	}
	if callbacks.Load() != 2 {
		t.Fatalf("output callbacks = %d, want one stdout and one stderr callback", callbacks.Load())
	}
	if result.CommandComplete.ExitCode == nil || *result.CommandComplete.ExitCode != 0 {
		t.Fatalf("completion = %+v, want exit 0", result.CommandComplete)
	}
	if got, want := string(result.Stderr), "Everything up-to-date\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	if got, want := string(result.Stdout), "GIT_PUSH_DRY_RUN_WITH_EXPLICIT_KEY_OK\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if result.OutputTruncated || shell.CapacityRetained() {
		t.Fatalf("successful status result truncated=%v retained_capacity=%v", result.OutputTruncated, shell.CapacityRetained())
	}

	followup, err := shell.RunScript(context.Background(), "command-bug013-followup", []byte("printf 'FOLLOWUP_OK\\n'\n"))
	if err != nil {
		t.Fatalf("follow-up after successful status command: %v", err)
	}
	if got, want := string(followup.Stdout), "FOLLOWUP_OK\n"; got != want {
		t.Fatalf("follow-up stdout = %q, want %q", got, want)
	}
}

// TestBUG013BlockedOutputDeliveryStillRetainsTheUnsafeBoundary proves the
// callback grace is bounded. A persistence write that never returns cannot
// keep a completed shell indefinitely usable or turn the request into a
// false success.
func TestBUG013BlockedOutputDeliveryStillRetainsTheUnsafeBoundary(t *testing.T) {
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{
		SessionID:  "session-bug013-blocked-delivery",
		Generation: "generation-bug013-blocked-delivery",
		Workspace:  t.TempDir(),
		// The short physical-boundary case is covered separately. This test
		// exercises a callback that has begun but does not return, so give the
		// drainer normal startup time before its bounded callback grace applies.
		OutputBoundaryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := shell.Close(); err != nil {
			t.Errorf("close persistent shell: %v", err)
		}
	}()

	callbackEntered := make(chan struct{}, 1)
	releaseCallback := make(chan struct{})
	defer close(releaseCallback)
	done := make(chan error, 1)
	started := time.Now()
	go func() {
		_, runErr := shell.RunScriptWithOutput(context.Background(), "command-bug013-blocked-delivery", []byte("printf 'OUTPUT_WAITING\\n'\n"), func(OutputChunk) error {
			select {
			case callbackEntered <- struct{}{}:
			default:
			}
			<-releaseCallback
			return nil
		})
		done <- runErr
	}()

	select {
	case <-callbackEntered:
	case runErr := <-done:
		t.Fatalf("run ended before output callback started: %v", runErr)
	case <-time.After(2 * time.Second):
		t.Fatal("output callback did not start")
	}

	select {
	case runErr := <-done:
		if !errors.Is(runErr, ErrOutputBoundary) {
			t.Fatalf("blocked output error = %v, want ErrOutputBoundary", runErr)
		}
	case <-time.After(maxOutputCallbackBoundaryGrace + 2*time.Second):
		t.Fatal("blocked output callback did not reach bounded unsafe result")
	}
	if elapsed := time.Since(started); elapsed < maxOutputCallbackBoundaryGrace {
		t.Fatalf("blocked output ended before callback grace elapsed: %s", elapsed)
	}
	if !shell.CapacityRetained() {
		t.Fatal("blocked output callback did not retain uncertain capacity")
	}
	if _, err := shell.RunScript(context.Background(), "command-bug013-blocked-delivery-after", []byte("printf 'must-not-run\\n'\n")); !errors.Is(err, ErrPersistentShellLost) {
		t.Fatalf("follow-up after blocked callback error = %v, want ErrPersistentShellLost", err)
	}
}

// TestBUG013OutputCallbackFailureRetainsTheUnsafeBoundary proves a failed
// durable output write is explicit. It is not a normal command result because
// the executor cannot prove that all output became authoritative.
func TestBUG013OutputCallbackFailureRetainsTheUnsafeBoundary(t *testing.T) {
	shell, err := StartPersistentShell(context.Background(), PersistentShellOptions{
		SessionID:  "session-bug013-callback-failure",
		Generation: "generation-bug013-callback-failure",
		Workspace:  t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := shell.Close(); err != nil {
			t.Errorf("close persistent shell: %v", err)
		}
	}()

	callbackFailure := errors.New("injected durable output failure")
	_, err = shell.RunScriptWithOutput(context.Background(), "command-bug013-callback-failure", []byte("printf 'OUTPUT_CALLBACK_FAILURE\\n'\n"), func(OutputChunk) error {
		return callbackFailure
	})
	if !errors.Is(err, ErrOutputCallback) {
		t.Fatalf("callback failure error = %v, want ErrOutputCallback", err)
	}
	if !shell.CapacityRetained() {
		t.Fatal("callback failure did not retain uncertain capacity")
	}
	if _, err := shell.RunScript(context.Background(), "command-bug013-callback-failure-after", []byte("printf 'must-not-run\\n'\n")); !errors.Is(err, ErrPersistentShellLost) {
		t.Fatalf("follow-up after callback failure error = %v, want ErrPersistentShellLost", err)
	}
}
