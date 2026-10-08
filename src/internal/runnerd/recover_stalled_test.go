package runnerd

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/execution"
	"remote-session-runner/src/internal/store"
)

func TestRunnerdRecoverStalledAuthorityOpenRejectsMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "remote.db")
	database, err := openRunnerdRecoveryAuthority(context.Background(), path)
	if database != nil {
		_ = database.Close()
		t.Fatal("runnerd recovery opened a missing authority")
	}
	if !errors.Is(err, store.ErrDatabaseMissing) {
		t.Fatalf("runnerd recovery missing authority error=%v, want %v", err, store.ErrDatabaseMissing)
	}
}

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

func TestParseLostRecoverySessionsAndCrossInputValidation(t *testing.T) {
	sessions, err := parseLostRecoverySessions([]string{"sess-one", "sess-two"})
	if err != nil || len(sessions) != 2 || sessions[0].SessionID != domain.SessionID("sess-one") || sessions[1].SessionID != domain.SessionID("sess-two") {
		t.Fatalf("parsed sessions=%+v err=%v", sessions, err)
	}
	for _, values := range [][]string{{"sess-one", "sess-one"}, {""}} {
		if _, err := parseLostRecoverySessions(values); err == nil {
			t.Fatalf("invalid sessions %q unexpectedly accepted", values)
		}
	}
	pairs, err := parseLostRecoveryPairs([]string{"sess-one:cmd-one"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateDistinctLostRecoverySessions(pairs, sessions); err == nil {
		t.Fatal("pair and commandless selection for the same session unexpectedly accepted")
	}
	if err := validateDistinctLostRecoverySessions(pairs, []execution.CommandlessLostRuntimeRecoveryRequest{{SessionID: "sess-three"}}); err != nil {
		t.Fatalf("distinct pair and commandless selections rejected: %v", err)
	}
}

func TestRunRecoverStalledUsageAcceptsLostSessionAsExplicitRecoveryInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"recover-stalled", "--config", "/fixture/linux.yaml", "--lost-session", "sess-commandless"}, &stdout, &stderr)
	if exit != 2 || !strings.Contains(stderr.String(), "--config, --apply") {
		t.Fatalf("recover-stalled lost-session without apply exit=%d stderr=%q, want explicit-apply usage error", exit, stderr.String())
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
