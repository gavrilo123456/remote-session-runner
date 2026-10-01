package runnerd

import (
	"bytes"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestRunRecoverStalledRequiresExplicitApply(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"recover-stalled", "--config", "/fixture/linux.yaml", "--job-id", "job-recover-stalled"}, &stdout, &stderr)
	if exit != 2 || !strings.Contains(stderr.String(), "--config, --apply") {
		t.Fatalf("recover-stalled exit=%d stderr=%q, want explicit-apply usage error", exit, stderr.String())
	}
}

func TestParseLostRecoveryPairsRejectsAmbiguousOrDuplicateInput(t *testing.T) {
	if _, err := parseLostRecoveryPairs([]string{"sess-one:cmd-one:extra"}); err == nil {
		t.Fatal("ambiguous pair unexpectedly accepted")
	}
	if _, err := parseLostRecoveryPairs([]string{"sess-one:cmd-one", "sess-one:cmd-two"}); err == nil {
		t.Fatal("duplicate session pair unexpectedly accepted")
	}
	parsed, err := parseLostRecoveryPairs([]string{"sess-one:cmd-one", "sess-two:cmd-two"})
	if err != nil || len(parsed) != 2 || parsed[0].SessionID != domain.SessionID("sess-one") || parsed[1].CommandID != domain.CommandID("cmd-two") {
		t.Fatalf("parsed pairs=%+v err=%v", parsed, err)
	}
}

func TestSameRecoveryJobIDsRequiresExactSet(t *testing.T) {
	expected := []domain.JobID{"job-one", "job-two"}
	if !sameRecoveryJobIDs([]domain.JobID{"job-two", "job-one"}, expected) {
		t.Fatal("same set in a different order was rejected")
	}
	if sameRecoveryJobIDs([]domain.JobID{"job-one", "job-three"}, expected) {
		t.Fatal("different set was accepted")
	}
}
