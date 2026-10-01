package mailbox

import (
	"encoding/json"
	"errors"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

// ReconciliationIssue carries only stable, non-secret fields suitable for an
// operational log. Its wrapped cause is deliberately excluded from Error()
// because a transport or filesystem error may contain arbitrary sensitive text.
type ReconciliationIssue struct {
	MailboxID     string
	Stage         string
	Operation     string
	RequestID     string
	JobID         string
	SessionID     string
	CommandID     string
	TargetProfile string
	FailureClass  string
	cause         error
}

func (e *ReconciliationIssue) Error() string {
	if e == nil {
		return "mailbox reconciliation failed"
	}
	return "mailbox reconciliation failed"
}

func (e *ReconciliationIssue) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func mailboxReconciliationIssue(record store.MailboxExchangeRecord, stage string, cause error) *ReconciliationIssue {
	issue := &ReconciliationIssue{
		MailboxID: record.MailboxID, Stage: stage, Operation: record.Operation,
		RequestID: record.RequestID, FailureClass: mailboxReconciliationFailureClass(cause), cause: cause,
	}
	if record.Selection != nil && record.Selection.Target.Kind() == domain.TargetKindRemote {
		issue.TargetProfile = record.Selection.Target.Profile()
	}
	var reference struct {
		JobID     string `json:"job_id"`
		SessionID string `json:"session_id"`
		CommandID string `json:"command_id"`
	}
	if json.Unmarshal(record.ResponseBytes, &reference) == nil {
		if value, err := domain.NewJobID(reference.JobID); err == nil {
			issue.JobID = string(value)
		}
		if value, err := domain.NewSessionID(reference.SessionID); err == nil {
			issue.SessionID = string(value)
		}
		if value, err := domain.NewCommandID(reference.CommandID); err == nil {
			issue.CommandID = string(value)
		}
	}
	if issue.JobID == "" && record.Operation == "run" {
		if value, err := domain.NewJobID(record.ResourceID); err == nil {
			issue.JobID = string(value)
		}
	}
	return issue
}

func mailboxCycleIssue(mailboxID, stage string, cause error) *ReconciliationIssue {
	return &ReconciliationIssue{
		MailboxID: mailboxID, Stage: stage, FailureClass: mailboxReconciliationFailureClass(cause), cause: cause,
	}
}

func mailboxStageError(mailboxID, stage string, cause error) error {
	if len(ReconciliationIssues(cause)) != 0 {
		return cause
	}
	return mailboxCycleIssue(mailboxID, stage, cause)
}

func mailboxReconciliationFailureClass(err error) string {
	var operationErr *SessionOperationError
	switch {
	case errors.As(err, &operationErr) && operationErr != nil && operationErr.Code == "resource_not_found":
		return "resource_not_found"
	case errors.As(err, &operationErr) && operationErr != nil && operationErr.Retryable:
		return "operation_retryable"
	case errors.As(err, &operationErr):
		return "operation_failed"
	case errors.Is(err, ErrOutboxResponse):
		return "stored_response_invalid"
	case errors.Is(err, ErrSessionProcessorConfiguration):
		return "reconciliation_invariant"
	case errors.Is(err, store.ErrMailboxResponseExpired):
		return "response_expired"
	default:
		return "reconciliation_failed"
	}
}

// ReconciliationIssues extracts safe issues from a joined error tree for the
// runner-local logger. Callers must never write the original error text.
func ReconciliationIssues(err error) []ReconciliationIssue {
	if err == nil {
		return nil
	}
	issues := make([]ReconciliationIssue, 0)
	var visit func(error)
	visit = func(current error) {
		if current == nil {
			return
		}
		if issue, ok := current.(*ReconciliationIssue); ok && issue != nil {
			issues = append(issues, ReconciliationIssue{
				MailboxID: issue.MailboxID, Stage: issue.Stage, Operation: issue.Operation,
				RequestID: issue.RequestID, JobID: issue.JobID, SessionID: issue.SessionID,
				CommandID: issue.CommandID, TargetProfile: issue.TargetProfile, FailureClass: issue.FailureClass,
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
