package queueworker

import (
	"errors"
	"log/slog"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
)

// logMacLocalCompletedTeardownFailure returns whether this is the narrow
// terminal cleanup boundary. Neither raw errors nor job scripts/output are
// diagnostic inputs. A valid metadata snapshot remains evidence, not cleanup
// authority, and an invalid snapshot must not become a success observation.
func logMacLocalCompletedTeardownFailure(logger *slog.Logger, job store.JobRecord, session store.SessionRecord, err error) bool {
	if err == nil || job.Target.Kind() != domain.TargetKindLocal || job.Controller.Type() != domain.ControllerTypeLocalUser ||
		job.Phase != store.JobPhaseLost || job.TeardownState != store.JobTeardownLost || job.TeardownReason != "runtime_cleanup_unconfirmed" ||
		job.CommandState == nil || (*job.CommandState != domain.CommandStateFailed && *job.CommandState != domain.CommandStateSucceeded) ||
		!errors.Is(err, execution.ErrStopUnconfirmed) {
		return false
	}
	if logger == nil {
		return true
	}
	attrs := []any{
		"component", "mac_local_executor",
		"job_id", boundedCleanupIdentity(string(job.JobID)),
		"session_id", boundedCleanupIdentity(string(job.SessionID)),
		"command_id", boundedCleanupIdentity(string(job.CommandID)),
		"lifecycle_phase", "completed_command_teardown",
		"reason", "runtime_cleanup_unconfirmed",
	}
	if session.SessionID == job.SessionID && safeCleanupIdentity(session.RuntimeGeneration) {
		attrs = append(attrs, "runtime_generation", session.RuntimeGeneration)
	}
	var carrier domain.RuntimeCleanupDiagnosticCarrier
	if errors.As(err, &carrier) {
		diagnostic := carrier.RuntimeCleanupDiagnostic()
		if domain.ValidateRuntimeCleanupDiagnostic(diagnostic) {
			attrs = append(attrs, "diagnostic_available", true, "failure_stage", diagnostic.Stage,
				"error_category", diagnostic.Code, "diagnostic", diagnostic)
			logger.Warn("Mac local completed-command teardown failed", attrs...)
			return true
		}
	}
	attrs = append(attrs, "diagnostic_available", false, "failure_stage", "unavailable", "error_category", "diagnostic_unavailable")
	logger.Warn("Mac local completed-command teardown failed", attrs...)
	return true
}

func boundedCleanupIdentity(value string) string {
	if !safeCleanupIdentity(value) {
		return "unavailable"
	}
	return value
}

func safeCleanupIdentity(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' && c != '.' {
			return false
		}
	}
	return true
}
