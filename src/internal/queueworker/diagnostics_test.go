package queueworker

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
	"remote-session-runner/src/internal/testfixture"
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

func TestQueueClaimFailureDiagnosticIsSanitizedAndBounded(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	secret := "SQL parameters, request payload and credential must not be logged"
	logQueueClaimFailure(logger, errors.New(secret), 1250*time.Microsecond)
	logged := output.String()
	for _, expected := range []string{
		"queue worker could not claim command",
		"operation=queue_claim",
		"component=runner-locald",
		"timestamp_utc=",
		"elapsed_ms=1.25",
		"failure_stage=unclassified",
		"error_category=logical",
		"reason=operation_failed",
	} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("queue-claim diagnostic %q does not contain %q", logged, expected)
		}
	}
	if strings.Contains(logged, secret) || strings.Contains(logged, "sqlite_primary_code") || strings.Contains(logged, "os_error_code") {
		t.Fatalf("queue-claim diagnostic exposed error text or absent typed codes: %q", logged)
	}
	if len(logged) > 900 {
		t.Fatalf("queue-claim diagnostic length=%d, want <=900", len(logged))
	}
}

func TestQueueClaimFailureDiagnosticIncludesSQLiteCodesWithoutRawError(t *testing.T) {
	root := testfixture.New(t)
	databasePath := filepath.Join(root.Path(), "state", "queue-claim-diagnostic.db")
	database, err := store.Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.ExecContext(t.Context(), "PRAGMA busy_timeout=0"); err != nil {
		t.Fatal(err)
	}
	claimer, err := store.NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	blockerDB, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blockerDB.Close() })
	blocker, err := blockerDB.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocker.Close() })
	if _, err := blocker.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = blocker.ExecContext(context.Background(), "ROLLBACK") })

	_, claimErr := claimer.StartNextDispatchableCommand(t.Context(), store.DefaultRunningCommandLimit)
	if claimErr == nil {
		t.Fatal("claim succeeded while independent SQLite connection held the writer reservation")
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&output, nil))
	logQueueClaimFailure(logger, claimErr, 2*time.Millisecond)
	logged := output.String()
	for _, expected := range []string{
		"failure_stage=begin_transaction",
		"error_category=sqlite_lock",
		"sqlite_primary_code=5",
		"sqlite_extended_code=5",
		"reason=operation_failed",
	} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("queue-claim diagnostic %q does not contain %q", logged, expected)
		}
	}
	for _, forbidden := range []string{claimErr.Error(), "database is locked", "BEGIN IMMEDIATE", "request payload"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("queue-claim diagnostic exposed raw failure data %q: %q", forbidden, logged)
		}
	}
}
