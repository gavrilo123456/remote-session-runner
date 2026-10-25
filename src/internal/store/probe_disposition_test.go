package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"remote-session-runner/src/internal/audit"
	"remote-session-runner/src/internal/domain"
)

func TestMarkCreatingDirectProbeLostPreservesUnknownOutcomeAndCapacity(t *testing.T) {
	ctx := context.Background()
	authority := pCommandlessLostRecoveryStore(t)
	jobID := domain.JobID("job-7fae7172e801f2e22e7d6fce6014403d")
	sessionID := domain.SessionID("sess-548ea3986a2b736f9643b2b402188830")
	commandID := domain.CommandID("cmd-7dc98e864e7abd3a9c2596c3c4fe9399")
	key := "catalog-probe-63471d41af07b7afaf577d6a0989cc03-linux-host"
	seedCreatingDirectProbe(t, authority, jobID, sessionID, commandID, key, catalogMaintenanceProbeScript)

	job, session, err := authority.MarkCreatingDirectProbeLost(ctx, jobID, sessionID, commandID, key, "linux-dev", "tomasz.walczuk", CatalogProbeOwnerAbandonedReason)
	if err != nil {
		t.Fatal(err)
	}
	if job.Phase != JobPhaseLost || job.CommandState != nil || job.ExitCode != nil || job.FinalEventSequence != nil ||
		job.OutputComplete || job.OutputTruncated || job.OutputUnavailableReason != "" || job.TeardownState != JobTeardownPending || job.TeardownReason != "" {
		t.Fatalf("job disposition=%+v; expected truthful lost state with unknown output and pending cleanup", job)
	}
	if session.State != domain.SessionStateLost || session.RuntimeGeneration != "" {
		t.Fatalf("session disposition=%+v; expected lost state without an invented runtime generation", session)
	}
	reservation, err := authority.GetSessionReservation(ctx, sessionID)
	if err != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
		t.Fatalf("disposition released capacity before runtime proof: reservation=%+v err=%v", reservation, err)
	}
	if commands, err := authority.ListSessionCommands(ctx, sessionID); err != nil || len(commands) != 0 {
		t.Fatalf("commands=%+v err=%v; disposition must not invent a command", commands, err)
	}
	lifecycle, err := authority.ListSessionLifecycle(ctx, sessionID)
	if err != nil || len(lifecycle) != 2 || lifecycle[1].PreviousState == nil || *lifecycle[1].PreviousState != domain.SessionStateCreating ||
		lifecycle[1].NewState != domain.SessionStateLost || lifecycle[1].Reason != CatalogProbeOwnerAbandonedReason {
		t.Fatalf("session lifecycle=%+v err=%v", lifecycle, err)
	}

	// A retry after an interrupted operator call is idempotent and retains the
	// same single disposition event; it never advances or releases capacity.
	job, session, err = authority.MarkCreatingDirectProbeLost(ctx, jobID, sessionID, commandID, key, "linux-dev", "tomasz.walczuk", CatalogProbeOwnerAbandonedReason)
	if err != nil || job.Phase != JobPhaseLost || session.State != domain.SessionStateLost {
		t.Fatalf("idempotent disposition job=%+v session=%+v err=%v", job, session, err)
	}
	lifecycle, err = authority.ListSessionLifecycle(ctx, sessionID)
	if err != nil || len(lifecycle) != 2 {
		t.Fatalf("retry appended another disposition: lifecycle=%+v err=%v", lifecycle, err)
	}
}

func TestMarkCreatingDirectProbeLostRejectsMismatchedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *AuthorityStore, domain.JobID, domain.SessionID)
		key    string
		script string
	}{
		{name: "wrong idempotency identity", key: "catalog-probe-other-linux-host", script: catalogMaintenanceProbeScript},
		{name: "different script", key: "catalog-probe-63471d41af07b7afaf577d6a0989cc03-linux-host", script: "printf unrelated"},
		{name: "runtime generation present", key: "catalog-probe-63471d41af07b7afaf577d6a0989cc03-linux-host", script: catalogMaintenanceProbeScript,
			mutate: func(t *testing.T, authority *AuthorityStore, _ domain.JobID, sessionID domain.SessionID) {
				if _, err := authority.db.ExecContext(context.Background(), `UPDATE exec_sessions SET runtime_generation = 'fixture-generation' WHERE session_id = ?`, string(sessionID)); err != nil {
					t.Fatal(err)
				}
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authority := pCommandlessLostRecoveryStore(t)
			jobID := domain.JobID("job-7fae7172e801f2e22e7d6fce6014403d")
			sessionID := domain.SessionID("sess-548ea3986a2b736f9643b2b402188830")
			commandID := domain.CommandID("cmd-7dc98e864e7abd3a9c2596c3c4fe9399")
			// Each subtest owns an isolated database; deterministic identities are
			// therefore safe to reuse and make the hostile evidence easy to read.
			validKey := "catalog-probe-63471d41af07b7afaf577d6a0989cc03-linux-host"
			seedCreatingDirectProbe(t, authority, jobID, sessionID, commandID, validKey, test.script)
			if test.mutate != nil {
				test.mutate(t, authority, jobID, sessionID)
			}
			_, _, err := authority.MarkCreatingDirectProbeLost(context.Background(), jobID, sessionID, commandID, test.key, "linux-dev", "tomasz.walczuk", CatalogProbeOwnerAbandonedReason)
			if !errors.Is(err, ErrProbeDispositionNotEligible) {
				t.Fatalf("disposition error=%v, want %v", err, ErrProbeDispositionNotEligible)
			}
			job, readErr := authority.GetJob(context.Background(), jobID)
			if readErr != nil || job.Phase != JobPhaseCreatingSession {
				t.Fatalf("rejected disposition changed job=%+v err=%v", job, readErr)
			}
		})
	}
}

func TestMarkCreatingDirectProbeLostRollsBackBothRecordsOnLifecycleFailure(t *testing.T) {
	ctx := context.Background()
	authority := pCommandlessLostRecoveryStore(t)
	jobID := domain.JobID("job-7fae7172e801f2e22e7d6fce6014403d")
	sessionID := domain.SessionID("sess-548ea3986a2b736f9643b2b402188830")
	commandID := domain.CommandID("cmd-7dc98e864e7abd3a9c2596c3c4fe9399")
	key := "catalog-probe-63471d41af07b7afaf577d6a0989cc03-linux-host"
	seedCreatingDirectProbe(t, authority, jobID, sessionID, commandID, key, catalogMaintenanceProbeScript)
	trigger := fmt.Sprintf(`CREATE TRIGGER abort_probe_disposition BEFORE INSERT ON exec_session_lifecycle WHEN NEW.reason = %q BEGIN SELECT RAISE(ABORT, 'fixture disposition failure'); END`, CatalogProbeOwnerAbandonedReason)
	if _, err := authority.db.ExecContext(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.MarkCreatingDirectProbeLost(ctx, jobID, sessionID, commandID, key, "linux-dev", "tomasz.walczuk", CatalogProbeOwnerAbandonedReason); err == nil {
		t.Fatal("disposition unexpectedly succeeded despite lifecycle write failure")
	}
	job, jobErr := authority.GetJob(ctx, jobID)
	session, sessionErr := authority.GetSession(ctx, sessionID)
	if jobErr != nil || sessionErr != nil || job.Phase != JobPhaseCreatingSession || session.State != domain.SessionStateCreating {
		t.Fatalf("partial disposition job=%+v jobErr=%v session=%+v sessionErr=%v", job, jobErr, session, sessionErr)
	}
}

func seedCreatingDirectProbe(t *testing.T, authority *AuthorityStore, jobID domain.JobID, sessionID domain.SessionID,
	commandID domain.CommandID, key, script string) {
	t.Helper()
	ctx := audit.WithIngress(context.Background(), audit.IngressDirectMTLS)
	jobInput := p024Acceptance(t, string(jobID), string(sessionID), string(commandID), key, script)
	if _, duplicate, err := authority.AcceptJob(ctx, jobInput); err != nil || duplicate {
		t.Fatalf("seed direct probe job duplicate=%v err=%v", duplicate, err)
	}
	sessionInput := p013Acceptance(t, string(sessionID), "catalog-probe-create-"+string(jobID), jobInput.Environment)
	if _, duplicate, err := authority.AcceptSessionCreate(ctx, sessionInput); err != nil || duplicate {
		t.Fatalf("seed direct probe session duplicate=%v err=%v", duplicate, err)
	}
}
