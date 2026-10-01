package dispatcher

import (
	"errors"
	"reflect"

	"remote-session-runner/src/internal/store"
)

// RemoteReconciliationIssue is a safe diagnostic wrapper for an accepted
// remote read. Its Error text deliberately excludes the underlying transport
// error because that error can contain arbitrary remote output or credentials.
// Unwrap remains available to code that needs the original error class.
type RemoteReconciliationIssue struct {
	Operation      string
	IntentID       string
	JobID          string
	SessionID      string
	CommandID      string
	TargetProfile  string
	ControllerType string
	ControllerID   string
	FailureClass   string
	RetryCount     int
	cause          error
}

func (e *RemoteReconciliationIssue) Error() string {
	if e == nil {
		return "remote reconciliation failed"
	}
	return "remote reconciliation failed"
}

func (e *RemoteReconciliationIssue) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func remoteReconciliationIssue(intent store.LocalIntentRecord, operation, failureClass string, retryCount int, cause error) *RemoteReconciliationIssue {
	if retryCount < 0 {
		retryCount = 0
	}
	return &RemoteReconciliationIssue{
		Operation: operation, IntentID: string(intent.IntentID), JobID: string(intent.JobID),
		SessionID: string(intent.SessionID), CommandID: string(intent.CommandID),
		TargetProfile: intent.Target.Profile(), ControllerType: string(intent.Controller.Type()), ControllerID: string(intent.Controller.ID()), FailureClass: failureClass,
		RetryCount: retryCount, cause: cause,
	}
}

func remoteReconciliationFailureClass(err error) string {
	switch {
	case errors.Is(err, ErrRemoteRouteUnavailable):
		return "remote_route_unavailable"
	case errors.Is(err, ErrRemoteEventCallerUnavailable):
		return "remote_event_unavailable"
	case errors.Is(err, ErrRemoteEventHistoryUnavailable):
		return "remote_event_history_unavailable"
	case errors.Is(err, ErrRemoteEventCursor):
		return "remote_event_cursor_invalid"
	case errors.Is(err, ErrRemoteTerminalUnconfirmed):
		return "remote_terminal_unconfirmed"
	case errors.Is(err, ErrRemoteResponse), errors.Is(err, ErrRemoteEventStream):
		return "remote_status_unavailable"
	case errors.Is(err, store.ErrRemoteProjectionInvalid), errors.Is(err, store.ErrRemoteProjectionConflict):
		return "remote_projection_invalid"
	default:
		return "remote_reconciliation_failed"
	}
}

func remoteReconciliationIssueFor(intent store.LocalIntentRecord, operation string, err error) error {
	var issue *RemoteReconciliationIssue
	if errors.As(err, &issue) {
		return err
	}
	return remoteReconciliationIssue(intent, operation, remoteReconciliationFailureClass(err), 0, err)
}

// RemoteReconciliationIssues returns every safe issue contained in an error
// tree. It is intended for operational logging only; callers must not log a
// raw error tree because a nested transport error can carry sensitive text.
func RemoteReconciliationIssues(err error) []RemoteReconciliationIssue {
	if err == nil {
		return nil
	}
	issues := make([]RemoteReconciliationIssue, 0)
	seen := make(map[error]struct{})
	var visit func(error)
	visit = func(current error) {
		if current == nil {
			return
		}
		// Go permits error implementations whose concrete value is not
		// comparable. Avoid turning a diagnostic fallback into a process panic
		// for such a transport error; comparable wrappers still get cycle
		// protection.
		if valueType := reflect.TypeOf(current); valueType != nil && valueType.Comparable() {
			if _, ok := seen[current]; ok {
				return
			}
			seen[current] = struct{}{}
		}
		if issue, ok := current.(*RemoteReconciliationIssue); ok && issue != nil {
			issues = append(issues, RemoteReconciliationIssue{
				Operation: issue.Operation, IntentID: issue.IntentID, JobID: issue.JobID,
				SessionID: issue.SessionID, CommandID: issue.CommandID, TargetProfile: issue.TargetProfile,
				ControllerType: issue.ControllerType, ControllerID: issue.ControllerID,
				FailureClass: issue.FailureClass, RetryCount: issue.RetryCount,
			})
			return
		}
		if joined, ok := current.(interface{ Unwrap() []error }); ok {
			for _, nested := range joined.Unwrap() {
				visit(nested)
			}
			return
		}
		if nested, ok := current.(interface{ Unwrap() error }); ok {
			visit(nested.Unwrap())
		}
	}
	visit(err)
	return issues
}
