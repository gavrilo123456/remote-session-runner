package runnerd

import (
	"testing"

	"remote-session-runner/src/internal/domain"
)

// A stalled-recovery attempt settles its proven cancelled jobs before it
// proves lost runtime boundaries. If a later lost-boundary proof fails, the
// identical explicit input must be accepted on retry: those job IDs are now
// terminal, while any remaining nonterminal jobs must still all be selected.
func TestSameRecoveryJobIDsAllowsPreviouslySettledExplicitJobsOnRetry(t *testing.T) {
	selected := []domain.JobID{"job-one", "job-two"}

	if !sameRecoveryJobIDs(nil, selected) {
		t.Fatal("retry after all selected jobs settled was rejected")
	}
	if !sameRecoveryJobIDs([]domain.JobID{"job-two"}, selected) {
		t.Fatal("retry after one selected job settled was rejected")
	}
	if !sameRecoveryJobIDs([]domain.JobID{"job-one", "job-two"}, selected) {
		t.Fatal("initial exact pending set was rejected")
	}
	if sameRecoveryJobIDs([]domain.JobID{"job-one", "job-other"}, selected) {
		t.Fatal("unselected pending job was accepted")
	}
}
