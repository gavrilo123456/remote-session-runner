package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
)

func TestCompletedTeardownRecoveryPreservesHistoryAndRetriesFinalization(t *testing.T) {
	ctx := context.Background()
	authority := pCommandlessLostRecoveryStore(t)
	pair, jobID := completedTeardownFixture(t, authority, "success")
	before := completedTeardownHistory(t, authority, pair, jobID)
	checked, err := authority.CheckCompletedTeardownRecoveryBatch(ctx, []CompletedTeardownRecoveryPair{pair})
	if err != nil || len(checked) != 1 || checked[0].AlreadyRecovered || checked[0].FinalizationPending || checked[0].JobID != jobID {
		t.Fatalf("initial check=%+v err=%v", checked, err)
	}
	if err := authority.CompleteCompletedTeardownRecoveryFinalization(ctx, pair); !errors.Is(err, ErrCompletedTeardownRecoveryNotReleasable) {
		t.Fatalf("finalization before release=%v", err)
	}
	if err := authority.ConfirmCompletedTeardownRecoveryBatch(ctx, []CompletedTeardownRecoveryPair{pair}); err != nil {
		t.Fatal(err)
	}
	reservation, err := authority.GetSessionReservation(ctx, pair.SessionID)
	if err != nil || reservation.CleanupConfirmedAt == nil || reservation.ReleasedAt == nil {
		t.Fatalf("session-only release=%+v err=%v", reservation, err)
	}
	pending, err := authority.ListPendingCompletedTeardownRecoveryFinalizations(ctx)
	if err != nil || len(pending) != 1 || pending[0].Pair != pair || !pending[0].AlreadyRecovered || !pending[0].FinalizationPending {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err := authority.ConfirmCompletedTeardownRecoveryBatch(ctx, []CompletedTeardownRecoveryPair{pair}); err != nil {
		t.Fatalf("confirmation retry: %v", err)
	}
	if after := completedTeardownHistory(t, authority, pair, jobID); !reflect.DeepEqual(before, after) {
		t.Fatalf("recovery rewrote command/job/session/event/lifecycle/slot history\nbefore=%#v\nafter=%#v", before, after)
	}
	if err := authority.CompleteCompletedTeardownRecoveryFinalization(ctx, pair); err != nil {
		t.Fatal(err)
	}
	if err := authority.CompleteCompletedTeardownRecoveryFinalization(ctx, pair); err != nil {
		t.Fatalf("completion retry: %v", err)
	}
	if pending, err := authority.ListPendingCompletedTeardownRecoveryFinalizations(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("completed pending=%+v err=%v", pending, err)
	}
	checked, err = authority.CheckCompletedTeardownRecoveryBatch(ctx, []CompletedTeardownRecoveryPair{pair})
	if err != nil || !checked[0].AlreadyRecovered || checked[0].FinalizationPending {
		t.Fatalf("completed no-op check=%+v err=%v", checked, err)
	}
}

func TestCompletedTeardownRecoveryAcceptsOrdinaryFailedCommandAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	authority := pCommandlessLostRecoveryStore(t)
	pair, jobID := completedTeardownFixtureWithOutcome(t, authority, "failed-exit-2", domain.CommandStateFailed, 2)
	before := completedTeardownHistory(t, authority, pair, jobID)
	events, err := authority.ListCommandEvents(ctx, pair.CommandID)
	if err != nil || len(events) == 0 || events[len(events)-1].Type != "command_failed" {
		t.Fatalf("failed command events=%+v err=%v", events, err)
	}
	checked, err := authority.CheckCompletedTeardownRecoveryBatch(ctx, []CompletedTeardownRecoveryPair{pair})
	command, commandErr := authority.GetCommand(ctx, pair.CommandID)
	if err != nil || commandErr != nil || len(checked) != 1 || checked[0].AlreadyRecovered || checked[0].Command.State != domain.CommandStateFailed || command.ExitCode == nil || *command.ExitCode != 2 {
		t.Fatalf("failed command check=%+v command=%+v err=%v command_err=%v", checked, command, err, commandErr)
	}
	if err := authority.ConfirmCompletedTeardownRecoveryBatch(ctx, []CompletedTeardownRecoveryPair{pair}); err != nil {
		t.Fatalf("confirm failed command cleanup: %v", err)
	}
	reservation, err := authority.GetSessionReservation(ctx, pair.SessionID)
	if err != nil || reservation.CleanupConfirmedAt == nil || reservation.ReleasedAt == nil {
		t.Fatalf("failed command session-only release=%+v err=%v", reservation, err)
	}
	if after := completedTeardownHistory(t, authority, pair, jobID); !reflect.DeepEqual(before, after) {
		t.Fatalf("recovery rewrote failed command/job/session/event/lifecycle/slot history\nbefore=%#v\nafter=%#v", before, after)
	}
	if afterEvents, err := authority.ListCommandEvents(ctx, pair.CommandID); err != nil || !reflect.DeepEqual(events, afterEvents) {
		t.Fatalf("recovery rewrote failed terminal event history: before=%+v after=%+v err=%v", events, afterEvents, err)
	}
}

func TestCompletedTeardownMigrationFromSchema36PreservesVerifiedReadOnlyPreflight(t *testing.T) {
	ctx := context.Background()
	path := completedTeardownSchema36AuthorityPath(t)
	before := b008ReadSchemaState(t, path)
	preflight, err := OpenExistingRestartPreflightReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := preflight.Close(); err != nil {
		t.Fatal(err)
	}
	if after := b008ReadSchemaState(t, path); !reflect.DeepEqual(before, after) {
		t.Fatal("schema36 preflight changed authority")
	}
	migrated, err := OpenExistingOfflineMaintenanceMigrating(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if version := readUserVersion(t, migrated); version != CurrentSchemaVersion {
		t.Fatalf("migrated version=%d", version)
	}
	var name string
	if err := migrated.QueryRowContext(ctx, `SELECT name FROM runner_schema_migrations WHERE version = 37`).Scan(&name); err != nil || name != "completed_teardown_recovery_finalizations" {
		t.Fatalf("migration37=%q err=%v", name, err)
	}
}

func TestCompletedTeardownRecoveryRejectsChangedBoundariesWithoutRelease(t *testing.T) {
	for _, test := range []struct{ name, sql string }{
		{"failed state without matching event", `UPDATE exec_commands SET state = 'failed', exit_code = 2`},
		{"nonzero exit", `UPDATE exec_commands SET exit_code = 1`},
		{"command incomplete", `UPDATE exec_commands SET output_complete = 0`},
		{"command truncated", `UPDATE exec_commands SET output_truncated = 1`},
		{"command unavailable", `UPDATE exec_commands SET output_unavailable_reason = 'retention_expired'`},
		{"job result mismatch", `UPDATE exec_jobs SET exit_code = 1`},
		{"job not lost", `UPDATE exec_jobs SET phase = 'complete'`},
		{"job teardown not lost", `UPDATE exec_jobs SET teardown_state = 'closed'`},
		{"job teardown reason", `UPDATE exec_jobs SET teardown_reason = 'different'`},
		{"controller mismatch", `UPDATE exec_jobs SET controller_id = 'different-owner'`},
		{"environment mismatch", `UPDATE exec_jobs SET environment = 'different-env'`},
		{"target mismatch", `UPDATE exec_jobs SET target_profile = 'different-profile'`},
		{"source mismatch", `UPDATE exec_jobs SET source_mode = 'git_revision', source_repository_alias = 'repo', source_requested_revision = 'dev'`},
		{"session not lost", `UPDATE exec_sessions SET state = 'ready'`},
		{"missing generation", `UPDATE exec_sessions SET runtime_generation = ''`},
		{"half released command", `UPDATE exec_command_slots SET released_at = NULL`},
		{"retained command", `UPDATE exec_command_slots SET stop_confirmed_at = NULL, released_at = NULL`},
		{"half released session", `UPDATE exec_capacity_reservations SET cleanup_confirmed_at = '2026-10-08T18:00:00Z'`},
		{"wrong event tail", `UPDATE exec_command_events SET event_type = 'command_failed' WHERE event_type = 'command_succeeded'`},
		{"failed command zero exit", `UPDATE exec_commands SET state = 'failed', exit_code = 0`},
		{"failed command missing exit", `UPDATE exec_commands SET state = 'failed', exit_code = NULL`},
		{"job cursor mismatch", `UPDATE exec_jobs SET final_event_sequence = final_event_sequence + 1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := pCommandlessLostRecoveryStore(t)
			pair, _ := completedTeardownFixture(t, authority, "reject")
			if strings.HasPrefix(test.name, "half released") {
				// Model a damaged legacy record rather than weakening production
				// constraints. The recovery must still refuse its partial boundary.
				connection, err := authority.db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := connection.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := connection.ExecContext(context.Background(), test.sql); err != nil {
					t.Fatal(err)
				}
				if _, err := connection.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = OFF`); err != nil {
					t.Fatal(err)
				}
				if err := connection.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := authority.db.Exec(test.sql); err != nil {
					t.Fatal(err)
				}
			}
			before, err := authority.GetSessionReservation(context.Background(), pair.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := authority.CheckCompletedTeardownRecoveryBatch(context.Background(), []CompletedTeardownRecoveryPair{pair}); !errors.Is(err, ErrCompletedTeardownRecoveryNotReleasable) {
				t.Fatalf("check=%v", err)
			}
			if err := authority.ConfirmCompletedTeardownRecoveryBatch(context.Background(), []CompletedTeardownRecoveryPair{pair}); !errors.Is(err, ErrCompletedTeardownRecoveryNotReleasable) {
				t.Fatalf("confirmation=%v", err)
			}
			after, _ := authority.GetSessionReservation(context.Background(), pair.SessionID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("refusal changed reservation")
			}
		})
	}
}

func TestCompletedTeardownFailedRecoveryRejectsInconsistentEvidence(t *testing.T) {
	for _, test := range []struct{ name, sql string }{
		{"wrong terminal event", `UPDATE exec_command_events SET event_type = 'command_succeeded' WHERE event_type = 'command_failed'`},
		{"timeout terminal event", `UPDATE exec_command_events SET event_type = 'command_timed_out' WHERE event_type = 'command_failed'`},
		{"zero exit", `UPDATE exec_commands SET exit_code = 0`},
		{"job exit mismatch", `UPDATE exec_jobs SET exit_code = 0`},
		{"job state mismatch", `UPDATE exec_jobs SET command_state = 'succeeded'`},
		{"incomplete output", `UPDATE exec_commands SET output_complete = 0`},
		{"event prefix gap", `DELETE FROM exec_command_events WHERE sequence = 2`},
		{"retained command slot", `UPDATE exec_command_slots SET stop_confirmed_at = NULL, released_at = NULL`},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := pCommandlessLostRecoveryStore(t)
			pair, _ := completedTeardownFixtureWithOutcome(t, authority, "failed-reject", domain.CommandStateFailed, 2)
			if _, err := authority.db.Exec(test.sql); err != nil {
				t.Fatal(err)
			}
			before, err := authority.GetSessionReservation(context.Background(), pair.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := authority.CheckCompletedTeardownRecoveryBatch(context.Background(), []CompletedTeardownRecoveryPair{pair}); !errors.Is(err, ErrCompletedTeardownRecoveryNotReleasable) {
				t.Fatalf("inconsistent evidence check=%v", err)
			}
			if err := authority.ConfirmCompletedTeardownRecoveryBatch(context.Background(), []CompletedTeardownRecoveryPair{pair}); !errors.Is(err, ErrCompletedTeardownRecoveryNotReleasable) {
				t.Fatalf("inconsistent evidence confirmation=%v", err)
			}
			after, err := authority.GetSessionReservation(context.Background(), pair.SessionID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal changed retained reservation before=%+v after=%+v err=%v", before, after, err)
			}
		})
	}
}

func TestCompletedTeardownRecoveryRejectsUnselectedWorkAndPendingFinalizations(t *testing.T) {
	for _, name := range []string{"unselected reservation", "queued command", "nonterminal job", "legacy pair finalization", "legacy commandless finalization", "unselected completed finalization", "sibling command"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			authority := pCommandlessLostRecoveryStore(t)
			pair, _ := completedTeardownFixture(t, authority, "selected")
			switch name {
			case "unselected reservation":
				completedTeardownFixture(t, authority, "unselected")
			case "queued command":
				session := p019ReadySession(t, authority, "sess-unselected-queued", "key-unselected-queued")
				p019Command(t, authority, session, "cmd-unselected-queued", "key-unselected-command")
			case "nonterminal job":
				if _, _, err := authority.AcceptJob(ctx, p024Acceptance(t, "job-unselected", "sess-unselected", "cmd-unselected", "key-unselected", "printf never")); err != nil {
					t.Fatal(err)
				}
			case "legacy pair finalization":
				session, command := pCommandlessLostPair(t, authority, "legacy-pair")
				if _, err := authority.db.Exec(`INSERT INTO exec_lost_runtime_recovery_finalizations (command_id, session_id, capacity_released_at) VALUES (?, ?, ?)`, string(command), string(session), "2026-10-08T18:00:00Z"); err != nil {
					t.Fatal(err)
				}
			case "legacy commandless finalization":
				other := pCommandlessLostRecoveryFixture(t, authority, "legacy-commandless")
				if _, err := authority.db.Exec(`INSERT INTO exec_commandless_lost_runtime_recovery_finalizations (session_id, job_id, capacity_released_at) VALUES (?, ?, ?)`, string(other.SessionID), string(other.JobID), "2026-10-08T18:00:00Z"); err != nil {
					t.Fatal(err)
				}
			case "unselected completed finalization":
				other, jobID := completedTeardownFixture(t, authority, "unselected-pending")
				if _, err := authority.db.Exec(`UPDATE exec_capacity_reservations SET cleanup_confirmed_at = ?, released_at = ? WHERE session_id = ?`, "2026-10-08T18:00:00Z", "2026-10-08T18:00:00Z", string(other.SessionID)); err != nil {
					t.Fatal(err)
				}
				if _, err := authority.db.Exec(`INSERT INTO exec_completed_teardown_recovery_finalizations (session_id, command_id, job_id, capacity_released_at) VALUES (?, ?, ?, ?)`, string(other.SessionID), string(other.CommandID), string(jobID), "2026-10-08T18:00:00Z"); err != nil {
					t.Fatal(err)
				}
			case "sibling command":
				if _, err := authority.db.Exec(`UPDATE exec_sessions SET state = 'ready' WHERE session_id = ?`, string(pair.SessionID)); err != nil {
					t.Fatal(err)
				}
				p019Command(t, authority, pair.SessionID, "cmd-sibling", "key-sibling")
				if _, err := authority.db.Exec(`UPDATE exec_sessions SET state = 'lost' WHERE session_id = ?`, string(pair.SessionID)); err != nil {
					t.Fatal(err)
				}
			}
			if err := authority.ConfirmCompletedTeardownRecoveryBatch(ctx, []CompletedTeardownRecoveryPair{pair}); !errors.Is(err, ErrCompletedTeardownRecoveryNotReleasable) {
				t.Fatalf("confirmation=%v", err)
			}
			reservation, _ := authority.GetSessionReservation(ctx, pair.SessionID)
			if reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
				t.Fatal("refusal released selected capacity")
			}
		})
	}
}

func TestCompletedTeardownRecoveryBatchRollbackAndExactSelection(t *testing.T) {
	ctx := context.Background()
	authority := pCommandlessLostRecoveryStore(t)
	first, _ := completedTeardownFixture(t, authority, "batch-first")
	second, _ := completedTeardownFixture(t, authority, "batch-second")
	for _, pairs := range [][]CompletedTeardownRecoveryPair{nil, {first}, {first, first}, {{SessionID: first.SessionID, CommandID: second.CommandID}}} {
		if err := authority.ConfirmCompletedTeardownRecoveryBatch(ctx, pairs); !errors.Is(err, ErrCompletedTeardownRecoveryNotReleasable) {
			t.Fatalf("unsafe selection %v: %v", pairs, err)
		}
	}
	if _, err := authority.db.Exec(fmt.Sprintf(`CREATE TRIGGER abort_completed_teardown BEFORE INSERT ON exec_completed_teardown_recovery_finalizations WHEN NEW.session_id = %q BEGIN SELECT RAISE(ABORT, 'fixture finalization failure'); END`, string(second.SessionID))); err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfirmCompletedTeardownRecoveryBatch(ctx, []CompletedTeardownRecoveryPair{first, second}); err == nil {
		t.Fatal("trigger did not abort")
	}
	for _, pair := range []CompletedTeardownRecoveryPair{first, second} {
		reservation, _ := authority.GetSessionReservation(ctx, pair.SessionID)
		if reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
			t.Fatal("batch partially released")
		}
	}
	if pending, err := authority.ListPendingCompletedTeardownRecoveryFinalizations(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("rollback pending=%v err=%v", pending, err)
	}
	if _, err := authority.db.Exec(`DROP TRIGGER abort_completed_teardown`); err != nil {
		t.Fatal(err)
	}
	if err := authority.ConfirmCompletedTeardownRecoveryBatch(ctx, []CompletedTeardownRecoveryPair{first, second}); err != nil {
		t.Fatal(err)
	}
}

func TestCompletedTeardownConfirmationRechecksInventoryAfterRuntimeProof(t *testing.T) {
	ctx := context.Background()
	authority := pCommandlessLostRecoveryStore(t)
	pair, _ := completedTeardownFixture(t, authority, "recheck")
	if _, err := authority.CheckCompletedTeardownRecoveryBatch(ctx, []CompletedTeardownRecoveryPair{pair}); err != nil {
		t.Fatal(err)
	}
	// A previous successful read/proof cannot authorize a later release after
	// unrelated work appeared. The immediate confirmation transaction decides.
	p019ReadySession(t, authority, "sess-after-proof", "key-after-proof")
	if err := authority.ConfirmCompletedTeardownRecoveryBatch(ctx, []CompletedTeardownRecoveryPair{pair}); !errors.Is(err, ErrCompletedTeardownRecoveryNotReleasable) {
		t.Fatalf("stale inventory confirmation=%v", err)
	}
	reservation, err := authority.GetSessionReservation(ctx, pair.SessionID)
	if err != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
		t.Fatalf("stale proof released reservation=%+v err=%v", reservation, err)
	}
}

func TestCompletedTeardownFinalizationSurvivesGCAndUnrelatedLiveWork(t *testing.T) {
	ctx := context.Background()
	clock := &p019Clock{value: time.Date(2026, 10, 8, 18, 0, 0, 0, time.UTC)}
	authority := newP019Store(t, clock)
	pair, jobID := completedTeardownFixture(t, authority, "gc")
	before := completedTeardownHistory(t, authority, pair, jobID)
	if err := authority.ConfirmCompletedTeardownRecoveryBatch(ctx, []CompletedTeardownRecoveryPair{pair}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(91 * 24 * time.Hour)
	if report, err := authority.CollectGarbage(ctx, GarbageCollectionOptions{}); err != nil || report.JobsDeleted != 0 || report.CommandsDeleted != 0 || report.SessionsDeleted != 0 || report.CommandsOutputExpired != 0 || report.CommandEventsDeleted != 0 {
		t.Fatalf("pending GC=%+v err=%v", report, err)
	}
	if after := completedTeardownHistory(t, authority, pair, jobID); !reflect.DeepEqual(before, after) {
		t.Fatal("GC mutated pending finalization proof")
	}
	p019ReadySession(t, authority, "sess-unrelated-active", "key-unrelated-active")
	if pending, err := authority.ListPendingCompletedTeardownRecoveryFinalizations(ctx); err != nil || len(pending) != 1 {
		t.Fatalf("startup pending=%+v err=%v", pending, err)
	}
	if err := authority.CompleteCompletedTeardownRecoveryFinalization(ctx, pair); err != nil {
		t.Fatalf("finalize with unrelated live work: %v", err)
	}
	if err := authority.CompleteCompletedTeardownRecoveryFinalization(ctx, pair); err != nil {
		t.Fatalf("repeat with unrelated live work: %v", err)
	}
	if _, err := authority.CollectGarbage(ctx, GarbageCollectionOptions{}); err != nil {
		t.Fatalf("GC after completion: %v", err)
	}
	if _, err := authority.GetJob(ctx, jobID); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("completed job GC=%v", err)
	}
}

func completedTeardownFixture(t *testing.T, authority *AuthorityStore, suffix string) (CompletedTeardownRecoveryPair, domain.JobID) {
	return completedTeardownFixtureWithOutcome(t, authority, suffix, domain.CommandStateSucceeded, 0)
}

func completedTeardownFixtureWithOutcome(t *testing.T, authority *AuthorityStore, suffix string, commandState domain.CommandState, exitCode int) (CompletedTeardownRecoveryPair, domain.JobID) {
	t.Helper()
	ctx := context.Background()
	pair := CompletedTeardownRecoveryPair{SessionID: domain.SessionID("sess-completed-" + suffix), CommandID: domain.CommandID("cmd-completed-" + suffix)}
	jobID := domain.JobID("job-completed-" + suffix)
	sessionInput := p013Acceptance(t, string(pair.SessionID), "key-completed-session-"+suffix, "mac-dev")
	script := "printf harmless"
	raw := []byte(`{"operation":"run","environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"source":{"mode":"empty"},"script":"printf harmless"}`)
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = authority.AcceptJob(ctx, JobAcceptance{JobID: jobID, SessionID: pair.SessionID, CommandID: pair.CommandID, Controller: sessionInput.SessionCreate.Controller, IdempotencyKey: "key-completed-job-" + suffix, RequestHash: hash, Environment: sessionInput.SessionCreate.Environment, Target: sessionInput.SessionCreate.Target, Source: sessionInput.SessionCreate.Source, Script: script, CanonicalPayload: canonical, IdempotencyRetention: DefaultSessionIdempotencyRetention})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := authority.AcceptSessionCreate(ctx, sessionInput); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.CompleteSessionCreation(ctx, pair.SessionID, domain.SessionStateReady, "generation-completed-"+suffix, "", "runtime_ready"); err != nil {
		t.Fatal(err)
	}
	p019Command(t, authority, pair.SessionID, pair.CommandID, "key-completed-command-"+suffix)
	if _, err := authority.StartNextEligibleCommand(ctx, DefaultRunningCommandLimit); err != nil {
		t.Fatal(err)
	}
	command, err := authority.CompleteRunningCommand(ctx, CommandTransition{CommandID: pair.CommandID, NextState: commandState, ExitCode: intPointer(exitCode), OutputComplete: true}, domain.SessionStateReady, "command_completed", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(ctx, pair.SessionID, domain.SessionStateClosing, "close_requested"); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionSession(ctx, pair.SessionID, domain.SessionStateLost, "runtime_cleanup_unconfirmed"); err != nil {
		t.Fatal(err)
	}
	teardown := JobTeardownLost
	if _, err := authority.CheckpointJob(ctx, jobID, JobCheckpoint{ExpectedPhase: JobPhaseCreatingSession, NextPhase: JobPhaseLost, Command: &command, TeardownState: &teardown, TeardownReason: "runtime_cleanup_unconfirmed"}); err != nil {
		t.Fatal(err)
	}
	return pair, jobID
}

func completedTeardownHistory(t *testing.T, authority *AuthorityStore, pair CompletedTeardownRecoveryPair, jobID domain.JobID) []any {
	t.Helper()
	ctx := context.Background()
	job, err := authority.GetJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := authority.GetSession(ctx, pair.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	command, events, err := authority.GetCommandWithEvents(ctx, pair.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authority.ListSessionLifecycle(ctx, pair.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	slot, err := authority.GetCommandSlot(ctx, pair.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	return []any{job, session, command, events, lifecycle, slot}
}

func completedTeardownSchema36AuthorityPath(t *testing.T) string {
	t.Helper()
	path := b008CurrentAuthorityPath(t)
	database, err := sql.Open("sqlite", existingCurrentDataSourceName(path, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, statement := range []string{
		`DROP TABLE lane_ingress_admission`,
		`DROP TABLE exec_completed_teardown_recovery_finalizations`,
		`DELETE FROM runner_schema_migrations WHERE version > 36`,
		`PRAGMA user_version = 36`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return path
}
