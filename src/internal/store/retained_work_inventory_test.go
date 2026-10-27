package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"remote-session-runner/src/internal/domain"
)

func TestRetainedWorkInventoryIncludesMixedWorkAndMailboxIdentitiesWithoutPayloads(t *testing.T) {
	ctx := context.Background()
	authority := pCommandlessLostRecoveryStore(t)
	lostSession, lostCommand := pCommandlessLostPair(t, authority, "inventory-held")
	commandless := pCommandlessLostRecoveryFixture(t, authority, "inventory-commandless")
	queuedSession := p019ReadySession(t, authority, "sess-inventory-queued", "key-inventory-queued")
	queued := p019Command(t, authority, queuedSession, "cmd-inventory-queued", "key-inventory-queued-command")
	job, _, err := authority.AcceptJob(ctx, p024Acceptance(t, "job-inventory-resumable", "sess-inventory-planned", "cmd-inventory-planned", "key-inventory-resumable", "SECRET_JOB_SCRIPT"))
	if err != nil {
		t.Fatal(err)
	}
	for i, resource := range []string{string(lostCommand), string(commandless.JobID), string(queued.CommandID)} {
		_, err = authority.db.Exec(`INSERT INTO mailbox_exchanges(exchange_id,mailbox_id,client_request_id,operation,controller_type,controller_id,canonical_hash_version,canonical_hash,canonical_payload,resource_id,request_state,created_at,updated_at) VALUES(?,?,?,'run','local_user','owner',1,zeroblob(32),'SECRET_MAILBOX_PAYLOAD',?,'accepted','2026-10-27T00:00:00Z','2026-10-27T00:00:00Z')`, fmt.Sprintf("exchange-inventory-%d", i), fmt.Sprintf("mailbox-inventory-%d", i), fmt.Sprintf("request-inventory-%d", i), resource)
		if err != nil {
			t.Fatal(err)
		}
	}
	for i, kind := range []string{"local", "remote"} {
		_, err = authority.db.Exec(`INSERT INTO local_intents(intent_id,operation,resource_id,session_id,target_kind,target_profile,environment,controller_type,controller_id,source_mode,request_hash_version,request_hash,idempotency_key,payload_json,script_bytes,script_sha256,delivery_state,reason,lease_owner,created_at,updated_at) VALUES(?,'create_session',?,?,?,'target-profile','environment-id','local_user','owner','empty',1,zeroblob(32),?,'SECRET_INTENT_PAYLOAD','SECRET_INTENT_SCRIPT',zeroblob(32),'uncertain','SECRET_INTENT_REASON','SECRET_LEASE_OWNER','2026-10-27T00:00:00Z','2026-10-27T00:00:00Z')`, fmt.Sprintf("intent-inventory-%d", i), fmt.Sprintf("resource-inventory-%d", i), fmt.Sprintf("session-intent-%d", i), kind, fmt.Sprintf("secret-idempotency-%d", i))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = authority.db.Exec(`UPDATE exec_commands SET script_bytes='SECRET_COMMAND_SCRIPT' WHERE command_id=?`, string(lostCommand)); err != nil {
		t.Fatal(err)
	}
	if _, err = authority.db.Exec(`UPDATE exec_command_events SET payload='SECRET_OUTPUT_PAYLOAD' WHERE command_id=?`, string(lostCommand)); err != nil {
		t.Fatal(err)
	}
	report, err := ReadRetainedWorkInventory(ctx, authority.db, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete || report.SchemaVersion != 40 || report.ObservedAt == "" || report.SnapshotStartedAt == "" || report.SnapshotCompletedAt == "" {
		t.Fatalf("incomplete snapshot: %+v", report)
	}
	if len(report.SessionReservations) != 3 || len(report.CommandSlots) != 1 || len(report.MailboxBindings) != 3 || len(report.LocalIntents) != 1 || len(report.RemoteIntents) != 1 {
		t.Fatalf("mixed inventory: %+v", report)
	}
	foundLost, foundQueued, foundCommandless, foundPlanned := false, false, false, false
	for _, row := range report.RetainedPairs {
		if row.SessionID == string(lostSession) && row.CommandID == string(lostCommand) && row.CommandPresent {
			foundLost = true
		}
	}
	for _, row := range report.Commands {
		if row.CommandID == string(queued.CommandID) && row.State == "queued" {
			foundQueued = true
		}
	}
	for _, row := range report.Jobs {
		if row.JobID == string(commandless.JobID) && !row.CommandPresent && row.PlannedCommandID == string(commandless.CommandID) {
			foundCommandless = true
		}
		if row.JobID == string(job.JobID) && row.Phase == "creating_session" && !row.CommandPresent {
			foundPlanned = true
		}
	}
	if !foundLost || !foundQueued || !foundCommandless || !foundPlanned {
		t.Fatalf("missing mixed identities: %+v", report)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SECRET_", "secret-idempotency", "script_bytes", "payload_json", "canonical_payload", "lease_owner", "source_path", "source_requested_revision", "teardown_reason", "exit_code"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("metadata export leaked %q: %s", forbidden, raw)
		}
	}
	pStalledRecoveryAssertLostPairRetained(t, authority, LostRuntimeRecoveryPair{SessionID: lostSession, CommandID: lostCommand})
}

func TestRetainedWorkInventoryIncludesEveryPendingFinalizationAfterCapacityRelease(t *testing.T) {
	authority := pCommandlessLostRecoveryStore(t)
	session, command := pCommandlessLostPair(t, authority, "inventory-final-pair")
	commandless := pCommandlessLostRecoveryFixture(t, authority, "inventory-final-commandless")
	completed, jobID := completedTeardownFixture(t, authority, "inventory-final-completed")
	const stamp = "2026-10-27T00:00:00Z"
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE exec_capacity_reservations SET cleanup_confirmed_at=?,released_at=?`, []any{stamp, stamp}},
		{`UPDATE exec_command_slots SET stop_confirmed_at=?,released_at=?`, []any{stamp, stamp}},
		{`INSERT INTO exec_lost_runtime_recovery_finalizations(session_id,command_id,capacity_released_at) VALUES(?,?,?)`, []any{string(session), string(command), stamp}},
		{`INSERT INTO exec_commandless_lost_runtime_recovery_finalizations(session_id,job_id,capacity_released_at) VALUES(?,?,?)`, []any{string(commandless.SessionID), string(commandless.JobID), stamp}},
		{`INSERT INTO exec_completed_teardown_recovery_finalizations(session_id,command_id,job_id,capacity_released_at) VALUES(?,?,?,?)`, []any{string(completed.SessionID), string(completed.CommandID), string(jobID), stamp}},
	} {
		if _, err := authority.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	report, err := ReadRetainedWorkInventory(context.Background(), authority.db, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete || len(report.RecoveryFinalizations) != 3 || len(report.Sessions) != 3 || len(report.Commands) != 2 || len(report.SessionReservations) != 0 || len(report.CommandSlots) != 0 {
		t.Fatalf("pending finalizations omitted after capacity release: %+v", report)
	}
	wantKinds := []string{"commandless", "completed_teardown", "lost_pair"}
	var gotKinds []string
	for _, f := range report.RecoveryFinalizations {
		gotKinds = append(gotKinds, f.Kind)
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("finalization kinds=%v", gotKinds)
	}
}

func TestRetainedWorkInventoryPairClassificationsNeverClaimProcessCleanup(t *testing.T) {
	authority := pCommandlessLostRecoveryStore(t)
	session, command := pCommandlessLostPair(t, authority, "inventory-class-lost")
	commandless := pCommandlessLostRecoveryFixture(t, authority, "inventory-class-commandless")
	completed, _ := completedTeardownFixture(t, authority, "inventory-class-completed")
	managed := p019ReadySession(t, authority, "sess-inventory-managed", "key-inventory-managed")
	report, err := ReadRetainedWorkInventory(context.Background(), authority.db, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{string(session): "lost_pair", string(commandless.SessionID): "commandless_lost", string(completed.SessionID): "completed_teardown", string(managed): "unclassified_metadata"}
	for _, pair := range report.RetainedPairs {
		if pair.Classification != want[pair.SessionID] {
			t.Fatalf("classification=%+v want=%s", pair, want[pair.SessionID])
		}
		if pair.SessionID == string(session) && (!pair.CommandSlotHeld || !pair.SessionReservationHeld || pair.CommandID != string(command)) {
			t.Fatalf("lost holds missing: %+v", pair)
		}
		if pair.SessionID == string(completed.SessionID) && (pair.CommandSlotHeld || !pair.SessionReservationHeld) {
			t.Fatalf("completed teardown must retain session only: %+v", pair)
		}
		delete(want, pair.SessionID)
	}
	if len(want) != 0 {
		t.Fatalf("missing retained classifications: %v", want)
	}
}

func TestRetainedWorkInventoryByteBoundAndCancellationReturnNoPartialRows(t *testing.T) {
	authority := pCommandlessLostRecoveryStore(t)
	commandless := pCommandlessLostRecoveryFixture(t, authority, "inventory-bytebound")
	if _, err := authority.db.Exec(`UPDATE exec_sessions SET runtime_generation=? WHERE session_id=?`, strings.Repeat("x", MaxRetainedWorkInventoryBytes), string(commandless.SessionID)); err != nil {
		t.Fatal(err)
	}
	report, err := ReadRetainedWorkInventory(context.Background(), authority.db, 0)
	if !errors.Is(err, ErrRetainedWorkInventoryLimit) || !reflect.DeepEqual(report, RetainedWorkInventory{}) {
		t.Fatalf("byte bound partial success: %+v err=%v", report, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err = ReadRetainedWorkInventory(ctx, authority.db, 0)
	if err == nil || !reflect.DeepEqual(report, RetainedWorkInventory{}) {
		t.Fatalf("cancelled inventory partial success: %+v err=%v", report, err)
	}
}

func TestRetainedWorkInventoryMismatchedPlannedCommandIsNotClassifiedCommandless(t *testing.T) {
	authority := pCommandlessLostRecoveryStore(t)
	_, otherCommand := pCommandlessLostPair(t, authority, "inventory-other-command")
	commandless := pCommandlessLostRecoveryFixture(t, authority, "inventory-misbound")
	if _, err := authority.db.Exec(`UPDATE exec_jobs SET command_id=? WHERE job_id=?`, string(otherCommand), string(commandless.JobID)); err != nil {
		t.Fatal(err)
	}
	report, err := ReadRetainedWorkInventory(context.Background(), authority.db, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, pair := range report.RetainedPairs {
		if pair.SessionID == string(commandless.SessionID) {
			found = true
			if pair.IdentityConsistent || !pair.CommandPresent || pair.Classification != "unclassified_metadata" {
				t.Fatalf("mismatched planned command misclassified: %+v", pair)
			}
		}
	}
	if !found {
		t.Fatal("mismatched planned command disappeared")
	}
}

func TestRetainedWorkInventoryUnfamiliarStatesAndOrphanCapacityAreVisible(t *testing.T) {
	authority := pCommandlessLostRecoveryStore(t)
	session, command := pCommandlessLostPair(t, authority, "inventory-unknown")
	connection, err := authority.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`PRAGMA ignore_check_constraints=ON`,
		`UPDATE exec_sessions SET state='future_state'`,
		`UPDATE exec_commands SET state='future_command'`,
		`PRAGMA ignore_check_constraints=OFF`,
		`PRAGMA foreign_keys=OFF`,
		`INSERT INTO exec_command_slots(command_id,host_key,reserved_at) VALUES('cmd-orphan-inventory','mac-workstation','2026-10-27T00:00:00Z')`,
		`INSERT INTO exec_capacity_reservations(session_id,host_key,reserved_at) VALUES('sess-orphan-inventory','mac-workstation','2026-10-27T00:00:00Z')`,
		`PRAGMA foreign_keys=ON`,
	} {
		if _, err := connection.ExecContext(context.Background(), query); err != nil {
			connection.Close()
			t.Fatal(err)
		}
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := ReadRetainedWorkInventory(context.Background(), authority.db, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundSession, foundCommand, foundOrphanSlot, foundOrphanReservation := false, false, false, false
	for _, row := range report.Sessions {
		if row.SessionID == string(session) && row.State == "future_state" {
			foundSession = true
		}
	}
	for _, row := range report.Commands {
		if row.CommandID == string(command) && row.State == "future_command" {
			foundCommand = true
		}
	}
	for _, row := range report.CommandSlots {
		if row.CommandID == "cmd-orphan-inventory" && !row.CommandPresent {
			foundOrphanSlot = true
		}
	}
	for _, row := range report.SessionReservations {
		if row.SessionID == "sess-orphan-inventory" && !row.SessionPresent {
			foundOrphanReservation = true
		}
	}
	if !foundSession || !foundCommand || !foundOrphanSlot || !foundOrphanReservation {
		t.Fatalf("unsafe metadata omitted: %+v", report)
	}
}

func TestRetainedWorkInventoryFailsClosedForMissingSchemaAndLimits(t *testing.T) {
	for _, test := range []struct {
		name, mutation string
		limit          int
		want           error
	}{
		{"missing-commandless", `DROP TABLE exec_commandless_lost_runtime_recovery_finalizations`, 0, nil},
		{"missing-completed", `DROP TABLE exec_completed_teardown_recovery_finalizations`, 0, nil},
		{"missing-lost", `DROP TABLE exec_lost_runtime_recovery_finalizations`, 0, nil},
		{"missing-mailbox", `DROP TABLE mailbox_exchanges`, 0, nil},
		{"missing-column", `ALTER TABLE exec_jobs DROP COLUMN ingress`, 0, nil},
		{"old-schema", `PRAGMA user_version=39`, 0, ErrSchemaVersion},
		{"later-schema", `PRAGMA user_version=41`, 0, ErrSchemaVersion},
		{"global-row-bound", "", 1, ErrRetainedWorkInventoryLimit},
		{"invalid-row-bound", "", MaxRetainedWorkInventoryRows + 1, ErrRetainedWorkInventoryLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := pCommandlessLostRecoveryStore(t)
			pCommandlessLostPair(t, authority, "inventory-incomplete")
			if test.mutation != "" {
				if _, err := authority.db.Exec(test.mutation); err != nil {
					t.Fatal(err)
				}
			}
			report, err := ReadRetainedWorkInventory(context.Background(), authority.db, test.limit)
			if err == nil || report.Complete || !reflect.DeepEqual(report, RetainedWorkInventory{}) {
				t.Fatalf("partial success: %+v err=%v", report, err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error=%v want %v", err, test.want)
			}
		})
	}
}

func TestRetainedWorkInventoryEmptyReadOnlyAndNoDatabaseWrites(t *testing.T) {
	ctx := context.Background()
	path := b008CurrentAuthorityPath(t)
	before := controlledRestartStatusFileState(t, path)
	database, err := OpenExistingCurrentReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ReadRetainedWorkInventory(ctx, database, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete || len(report.Sessions) != 0 || len(report.Commands) != 0 || len(report.RecoveryFinalizations) != 0 {
		t.Fatalf("empty inventory: %+v", report)
	}
	if _, err = database.Exec(`CREATE TABLE forbidden_inventory_write(id TEXT)`); err == nil {
		t.Fatal("diagnostic handle allowed writes")
	}
	if err = database.Close(); err != nil {
		t.Fatal(err)
	}
	if after := controlledRestartStatusFileState(t, path); !reflect.DeepEqual(before[""], after[""]) {
		t.Fatalf("read-only export changed database: before=%+v after=%+v", before, after)
	}
}

func TestRetainedWorkInventoryReadCutStaysConsistentWhileWriterCommits(t *testing.T) {
	ctx := context.Background()
	privateRoot := t.TempDir()
	if err := os.Chmod(privateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(privateRoot, "authority.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	writer, err := OpenExistingCurrent(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	authority, err := NewAuthorityStore(writer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = withReadTransaction(ctx, database, func(ctx context.Context, connection *sql.Conn) (bool, error) {
		if err := verifyCurrentSchemaConnection(ctx, connection); err != nil {
			return false, err
		}
		// A concurrent committed session must not appear midway through this cut.
		p019ReadySession(t, authority, domain.SessionID("sess-inventory-concurrent"), "key-inventory-concurrent")
		report, err := readRetainedWorkInventorySnapshot(ctx, connection, 100)
		if err != nil {
			return false, err
		}
		if len(report.Sessions) != 0 || len(report.SessionReservations) != 0 {
			t.Fatalf("torn read cut: %+v", report)
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := ReadRetainedWorkInventory(ctx, database, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Sessions) != 1 || len(report.SessionReservations) != 1 {
		t.Fatalf("next read cut missed committed work: %+v", report)
	}
}
