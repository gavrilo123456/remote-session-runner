package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
)

type scopedTeardownFixture struct {
	authority *AuthorityStore
	target    CompletedTeardownRecoveryPair
	targetJob domain.JobID
	peers     []CompletedTeardownRecoveryPair
	peerJobs  []domain.JobID
}

func newScopedTeardownFixture(t *testing.T) scopedTeardownFixture {
	t.Helper()
	f := scopedTeardownFixture{authority: pCommandlessLostRecoveryStore(t)}
	for index := range 7 {
		state, exit := domain.CommandStateFailed, 2
		if index < 2 {
			state, exit = domain.CommandStateSucceeded, 0
		}
		pair, job := completedTeardownFixtureWithOutcome(t, f.authority, fmt.Sprintf("scoped-peer-%d", index), state, exit)
		f.peers, f.peerJobs = append(f.peers, pair), append(f.peerJobs, job)
	}
	f.target, f.targetJob = completedTeardownFixtureWithOutcome(t, f.authority, "scoped-selected", domain.CommandStateFailed, 2)
	lost, job := completedTeardownFixtureWithOutcome(t, f.authority, "scoped-lost-peer", domain.CommandStateFailed, 2)
	for _, statement := range []string{
		`UPDATE exec_commands SET state='lost',exit_code=NULL,output_complete=0,output_unavailable_reason='capture_boundary_unconfirmed' WHERE command_id=?`,
		`UPDATE exec_jobs SET command_state='lost',exit_code=NULL,output_complete=0,output_unavailable_reason='capture_boundary_unconfirmed' WHERE command_id=?`,
		`UPDATE exec_command_events SET event_type='command_lost',payload=X'' WHERE command_id=? AND sequence=(SELECT final_event_sequence FROM exec_commands WHERE command_id=?)`,
		`UPDATE exec_command_slots SET stop_confirmed_at=NULL,released_at=NULL WHERE command_id=?`,
	} {
		args := []any{string(lost.CommandID)}
		if strings.Contains(statement, "SELECT final_event_sequence") {
			args = append(args, string(lost.CommandID))
		}
		if _, err := f.authority.db.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	f.peers, f.peerJobs = append(f.peers, lost), append(f.peerJobs, job)
	if _, err := f.authority.db.Exec(`INSERT INTO mailbox_exchanges
(exchange_id,mailbox_id,client_request_id,operation,controller_type,controller_id,canonical_hash_version,canonical_hash,canonical_payload,resource_id,request_state,response_revision,terminal_response_bytes,terminal_response_sha256,available_event_sequence,created_at,updated_at,response_bytes,response_sha256)
VALUES ('scoped-exchange','scoped-mailbox','req-scoped-original','run','local_user','tomasz.walczuk',1,zeroblob(32),X'7b7d',?,'complete',4,X'707265736572766564',zeroblob(32),8,'now','now',X'707265736572766564',zeroblob(32))`, string(f.targetJob)); err != nil {
		t.Fatal(err)
	}
	return f
}

func scopedTeardownHistory(t *testing.T, f scopedTeardownFixture) []any {
	t.Helper()
	values := completedTeardownHistory(t, f.authority, f.target, f.targetJob)
	for index, pair := range f.peers {
		values = append(values, completedTeardownHistory(t, f.authority, pair, f.peerJobs[index]))
		reservation, err := f.authority.GetSessionReservation(context.Background(), pair.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, reservation)
	}
	var response, terminal []byte
	var acknowledged *string
	var revision, cursor int
	if err := f.authority.db.QueryRow(`SELECT response_bytes,terminal_response_bytes,acknowledged_at,response_revision,available_event_sequence FROM mailbox_exchanges WHERE exchange_id='scoped-exchange'`).Scan(&response, &terminal, &acknowledged, &revision, &cursor); err != nil {
		t.Fatal(err)
	}
	return append(values, response, terminal, acknowledged, revision, cursor)
}

func TestScopedCompletedTeardownPreservesEightMixedPeersAndRetriesFinalization(t *testing.T) {
	ctx := context.Background()
	f := newScopedTeardownFixture(t)
	before := scopedTeardownHistory(t, f)
	plan, err := f.authority.ReadScopedCompletedTeardownRecoveryPlan(ctx, f.target)
	if err != nil || len(plan.PreservedSessionIDs) != 8 || len(plan.Sessions) != 9 || !validScopedPreservationSHA(plan.PreservedInventorySHA256) || plan.Recovery.AlreadyRecovered {
		t.Fatalf("wrong scoped plan: %+v err=%v", plan, err)
	}
	if _, err := f.authority.CheckCompletedTeardownRecoveryBatch(ctx, []CompletedTeardownRecoveryPair{f.target}); !errors.Is(err, ErrCompletedTeardownRecoveryNotReleasable) {
		t.Fatalf("ordinary whole-set guard was weakened: %v", err)
	}
	for _, expected := range []string{"", strings.Repeat("0", 64), strings.Repeat("A", 64)} {
		if err := f.authority.ConfirmScopedCompletedTeardownRecovery(ctx, f.target, expected); err == nil {
			t.Fatalf("invalid preservation hash was accepted: %q", expected)
		}
	}
	if err := f.authority.ConfirmScopedCompletedTeardownRecovery(ctx, f.target, plan.PreservedInventorySHA256); err != nil {
		t.Fatal(err)
	}
	pending, err := f.authority.ReadScopedCompletedTeardownRecoveryPlan(ctx, f.target)
	if err != nil || !pending.Recovery.AlreadyRecovered || !pending.Recovery.FinalizationPending || pending.PreservedInventorySHA256 != plan.PreservedInventorySHA256 {
		t.Fatalf("interrupted release/finalization state is not resumable: %+v err=%v", pending, err)
	}
	if err := f.authority.ConfirmScopedCompletedTeardownRecovery(ctx, f.target, plan.PreservedInventorySHA256); err != nil {
		t.Fatalf("pending retry failed: %v", err)
	}
	if err := f.authority.CompleteScopedCompletedTeardownRecoveryFinalization(ctx, f.target, plan.PreservedInventorySHA256); err != nil {
		t.Fatal(err)
	}
	final, err := f.authority.ReadScopedCompletedTeardownRecoveryPlan(ctx, f.target)
	if err != nil || !final.Recovery.AlreadyRecovered || final.Recovery.FinalizationPending || final.PreservedInventorySHA256 != plan.PreservedInventorySHA256 {
		t.Fatalf("fully finalized repeat changed preservation contract: %+v err=%v", final, err)
	}
	if err := f.authority.ConfirmScopedCompletedTeardownRecovery(ctx, f.target, plan.PreservedInventorySHA256); err != nil {
		t.Fatalf("fully finalized repeat failed: %v", err)
	}
	if err := f.authority.CompleteScopedCompletedTeardownRecoveryFinalization(ctx, f.target, plan.PreservedInventorySHA256); err != nil {
		t.Fatalf("fully finalized finalization retry failed: %v", err)
	}
	if after := scopedTeardownHistory(t, f); !reflect.DeepEqual(before, after) {
		t.Fatal("scoped release changed original result/events/ACK or an unselected hold")
	}
	if len(final.PreservedSessionIDs) != 8 || len(final.Sessions) != 9 {
		t.Fatal("post-release plan omitted preserved holds or selected historical identity")
	}
	var heldReservations, heldCommands, finalizations int
	for _, query := range []struct {
		sql string
		out *int
	}{
		{`SELECT COUNT(*) FROM exec_capacity_reservations WHERE released_at IS NULL`, &heldReservations},
		{`SELECT COUNT(*) FROM exec_command_slots WHERE released_at IS NULL`, &heldCommands},
		{`SELECT COUNT(*) FROM exec_completed_teardown_recovery_finalizations`, &finalizations},
	} {
		if err := f.authority.db.QueryRow(query.sql).Scan(query.out); err != nil {
			t.Fatal(err)
		}
	}
	if heldReservations != 8 || heldCommands != 1 || finalizations != 0 {
		t.Fatalf("wrong exact capacity delta: reservations=%d commands=%d pending=%d", heldReservations, heldCommands, finalizations)
	}
	raw, err := json.Marshal(plan)
	if err != nil || strings.Contains(string(raw), "RuntimeGeneration") || strings.Contains(string(raw), "command_failed") || strings.Contains(string(raw), "printf harmless") {
		t.Fatalf("in-memory execution records leaked into plan JSON: %s err=%v", raw, err)
	}
}

func TestScopedCompletedTeardownRefusesStaleMetadataAndInvalidBoundaries(t *testing.T) {
	for _, kind := range []string{"unselected-result", "peer-identity-mismatch", "invalid-text-metadata", "blob-text-metadata", "selected-event", "ACK", "active-session", "queued-command", "missing-generation", "incomplete-output", "unselected-finalization", "orphan-reservation"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			f := newScopedTeardownFixture(t)
			plan, err := f.authority.ReadScopedCompletedTeardownRecoveryPlan(ctx, f.target)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unselected-result":
				_, err = f.authority.db.Exec(`UPDATE exec_jobs SET updated_at='2026-10-09T00:00:00Z' WHERE job_id=?`, string(f.peerJobs[0]))
			case "peer-identity-mismatch":
				_, err = f.authority.db.Exec(`UPDATE exec_jobs SET controller_id='different-owner' WHERE job_id=?`, string(f.peerJobs[7]))
			case "invalid-text-metadata":
				_, err = f.authority.db.Exec(`UPDATE exec_session_lifecycle SET reason=CAST(X'ff' AS TEXT) WHERE session_id=?`, string(f.peers[0].SessionID))
			case "blob-text-metadata":
				_, err = f.authority.db.Exec(`UPDATE exec_session_lifecycle SET reason=X'ff' WHERE session_id=?`, string(f.peers[0].SessionID))
			case "selected-event":
				_, err = f.authority.db.Exec(`UPDATE exec_command_events SET occurred_at='2026-10-09T00:00:00Z' WHERE command_id=?`, string(f.target.CommandID))
			case "ACK":
				_, err = f.authority.db.Exec(`UPDATE mailbox_exchanges SET acknowledged_at='2026-10-09T00:00:00Z' WHERE exchange_id='scoped-exchange'`)
			case "active-session":
				p019ReadySession(t, f.authority, "sess-scoped-new-active", "key-scoped-new-active")
			case "queued-command":
				_, err = f.authority.db.Exec(`UPDATE exec_commands SET state='queued' WHERE command_id=?`, string(f.peers[0].CommandID))
			case "missing-generation":
				_, err = f.authority.db.Exec(`UPDATE exec_sessions SET runtime_generation='' WHERE session_id=?`, string(f.target.SessionID))
			case "incomplete-output":
				_, err = f.authority.db.Exec(`UPDATE exec_commands SET output_complete=0 WHERE command_id=?`, string(f.target.CommandID))
			case "unselected-finalization":
				_, err = f.authority.db.Exec(`INSERT INTO exec_completed_teardown_recovery_finalizations(session_id,command_id,job_id,capacity_released_at) VALUES (?,?,?,'2026-10-09T00:00:00Z')`, string(f.peers[0].SessionID), string(f.peers[0].CommandID), string(f.peerJobs[0]))
			case "orphan-reservation":
				// Existing databases can contain orphan metadata after corruption;
				// the recovery must refuse rather than omit it from its exact set.
				conn, openErr := f.authority.db.Conn(ctx)
				if openErr != nil {
					t.Fatal(openErr)
				}
				_, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`)
				if err == nil {
					_, err = conn.ExecContext(ctx, `INSERT INTO exec_capacity_reservations(session_id,host_key,reserved_at) VALUES ('sess-scoped-orphan',?,'2026-10-09T00:00:00Z')`, reservationHostKey)
				}
				_, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
				_ = conn.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := f.authority.ConfirmScopedCompletedTeardownRecovery(ctx, f.target, plan.PreservedInventorySHA256); err == nil {
				t.Fatal("changed or ineligible authority released capacity")
			}
			if kind == "peer-identity-mismatch" || strings.HasSuffix(kind, "text-metadata") {
				if _, err := f.authority.ReadScopedCompletedTeardownRecoveryPlan(ctx, f.target); err == nil {
					t.Fatal("ambiguous retained peer was accepted as a new baseline")
				}
			}
			reservation, err := f.authority.GetSessionReservation(ctx, f.target.SessionID)
			if err != nil || reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
				t.Fatalf("refusal changed selected capacity: %+v err=%v", reservation, err)
			}
		})
	}
}

func TestScopedCompletedTeardownRollsBackFailureAndCollateralChanges(t *testing.T) {
	for _, kind := range []string{"finalization-insert", "collateral-update", "collateral-payload"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			f := newScopedTeardownFixture(t)
			before := scopedTeardownHistory(t, f)
			plan, err := f.authority.ReadScopedCompletedTeardownRecoveryPlan(ctx, f.target)
			if err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER scoped_recovery_failure BEFORE INSERT ON exec_completed_teardown_recovery_finalizations BEGIN SELECT RAISE(ABORT,'fixture finalization failure'); END`
			if kind == "collateral-update" {
				trigger = fmt.Sprintf(`CREATE TRIGGER scoped_recovery_failure AFTER UPDATE ON exec_capacity_reservations WHEN NEW.session_id=%q BEGIN UPDATE exec_jobs SET updated_at='2026-10-09T00:00:00Z' WHERE job_id=%q; END`, string(f.target.SessionID), string(f.peerJobs[0]))
			}
			if kind == "collateral-payload" {
				trigger = fmt.Sprintf(`CREATE TRIGGER scoped_recovery_failure AFTER UPDATE ON exec_capacity_reservations WHEN NEW.session_id=%q BEGIN UPDATE exec_jobs SET script_bytes=X'00' WHERE job_id=%q; END`, string(f.target.SessionID), string(f.peerJobs[0]))
			}
			if _, err := f.authority.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			if err := f.authority.ConfirmScopedCompletedTeardownRecovery(ctx, f.target, plan.PreservedInventorySHA256); err == nil {
				t.Fatal("transaction accepted an insertion failure or collateral change")
			}
			reservation, _ := f.authority.GetSessionReservation(ctx, f.target.SessionID)
			if reservation.CleanupConfirmedAt != nil || reservation.ReleasedAt != nil {
				t.Fatal("failed transaction partially released the selected reservation")
			}
			if after := scopedTeardownHistory(t, f); !reflect.DeepEqual(before, after) {
				t.Fatal("failed transaction left collateral history changes")
			}
			var pending int
			if err := f.authority.db.QueryRow(`SELECT COUNT(*) FROM exec_completed_teardown_recovery_finalizations`).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("failed transaction retained finalization: %d err=%v", pending, err)
			}
		})
	}
}

func TestScopedCompletedTeardownFinalizationRefusesCollateralPayloadChanges(t *testing.T) {
	ctx := context.Background()
	f := newScopedTeardownFixture(t)
	plan, err := f.authority.ReadScopedCompletedTeardownRecoveryPlan(ctx, f.target)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.authority.CompleteScopedCompletedTeardownRecoveryFinalization(ctx, f.target, plan.PreservedInventorySHA256); !errors.Is(err, ErrCompletedTeardownRecoveryNotReleasable) {
		t.Fatalf("unreleased target finalized: %v", err)
	}
	if err := f.authority.ConfirmScopedCompletedTeardownRecovery(ctx, f.target, plan.PreservedInventorySHA256); err != nil {
		t.Fatal(err)
	}
	before := scopedTeardownHistory(t, f)
	trigger := fmt.Sprintf(`CREATE TRIGGER scoped_finalize_failure AFTER DELETE ON exec_completed_teardown_recovery_finalizations WHEN OLD.session_id=%q BEGIN UPDATE exec_jobs SET script_bytes=X'00' WHERE job_id=%q; END`, string(f.target.SessionID), string(f.peerJobs[0]))
	if _, err := f.authority.db.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	if err := f.authority.CompleteScopedCompletedTeardownRecoveryFinalization(ctx, f.target, plan.PreservedInventorySHA256); err == nil {
		t.Fatal("finalization accepted a collateral payload write")
	}
	if after := scopedTeardownHistory(t, f); !reflect.DeepEqual(before, after) {
		t.Fatal("failed finalization changed preserved history")
	}
	pending, err := f.authority.ReadScopedCompletedTeardownRecoveryPlan(ctx, f.target)
	if err != nil || !pending.Recovery.AlreadyRecovered || !pending.Recovery.FinalizationPending || pending.PreservedInventorySHA256 != plan.PreservedInventorySHA256 {
		t.Fatalf("failed finalization did not preserve retry state: %+v err=%v", pending, err)
	}
	if _, err := f.authority.db.Exec(`DROP TRIGGER scoped_finalize_failure`); err != nil {
		t.Fatal(err)
	}
	if err := f.authority.CompleteScopedCompletedTeardownRecoveryFinalization(ctx, f.target, plan.PreservedInventorySHA256); err != nil {
		t.Fatal(err)
	}
}
