package queueworker

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
)

func TestMacLocalStartupDiagnosticIsScopedAndBounded(t *testing.T) {
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	job := store.JobRecord{
		JobID:       "job-diagnostic-test",
		SessionID:   "sess-diagnostic-test",
		Controller:  controller,
		Target:      target,
		Source:      domain.NewEmptySource(),
		ScriptBytes: []byte("script payload must never enter diagnostic logs"),
	}
	startupErr := fmt.Errorf("%w: launch agent failed during readiness", execution.ErrRuntimeUnavailable)

	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	logMacLocalStartupFailure(logger, job, startupErr)
	logged := output.String()
	for _, expected := range []string{"Mac local empty-source startup failed", "job_id=job-diagnostic-test", "session_id=sess-diagnostic-test", "reason=runtime_unavailable", "launch agent failed during readiness"} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("diagnostic %q does not contain %q", logged, expected)
		}
	}
	if strings.Contains(logged, string(job.ScriptBytes)) {
		t.Fatalf("diagnostic exposed the script payload: %q", logged)
	}

	output.Reset()
	remote := job
	remote.Target, err = domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	logMacLocalStartupFailure(logger, remote, startupErr)
	logMacLocalStartupFailure(logger, job, fmt.Errorf("unrelated failure"))
	if output.Len() != 0 {
		t.Fatalf("diagnostic logged outside the Mac-local empty-source runtime failure: %q", output.String())
	}

	longErr := fmt.Errorf("%w: %s", execution.ErrRuntimeUnavailable, strings.Repeat("x", 600))
	output.Reset()
	logMacLocalStartupFailure(logger, job, longErr)
	if got := len(output.String()); got > 900 {
		t.Fatalf("bounded diagnostic length=%d, want <=900", got)
	}
}
