package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP024I04AcceptsDurableJobStableIDsAndReopens(t *testing.T) {
	root := testfixture.New(t)
	databasePath := root.Path() + "/state/p024-reopen.db"
	now := time.Date(2026, 9, 26, 18, 0, 0, 0, time.UTC)
	db, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	input := p024Acceptance(t, "job-p024-reopen", "session-p024-reopen", "command-p024-reopen", "run-key-p024-reopen", "printf 'exact job bytes\\n'")
	record, duplicate, err := store.AcceptJob(context.Background(), input)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if duplicate {
		db.Close()
		t.Fatal("first job reported duplicate")
	}
	if record.JobID != input.JobID || record.SessionID != input.SessionID || record.CommandID != input.CommandID || record.Phase != JobPhaseCreatingSession || record.TeardownState != JobTeardownPending {
		db.Close()
		t.Fatalf("accepted job = %+v", record)
	}
	if string(record.CanonicalPayload) != string(input.CanonicalPayload) || string(record.ScriptBytes) != input.Script || record.RequestHash.String() != input.RequestHash.String() {
		db.Close()
		t.Fatalf("accepted immutable payload changed: %+v", record)
	}
	if !record.CreatedAt.Equal(now) || !record.UpdatedAt.Equal(now) {
		db.Close()
		t.Fatalf("accepted timestamps = created %s updated %s, want %s", record.CreatedAt, record.UpdatedAt, now)
	}
	var schemaVersion int
	if err := db.QueryRow("PRAGMA user_version").Scan(&schemaVersion); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if schemaVersion != CurrentSchemaVersion {
		db.Close()
		t.Fatalf("schema version = %d, want %d", schemaVersion, CurrentSchemaVersion)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedDB, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened, err := NewAuthorityStoreWithClock(reopenedDB, func() time.Time { return now.Add(time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	reopenedRecord, err := reopened.GetJob(context.Background(), input.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if reopenedRecord.JobID != input.JobID || reopenedRecord.SessionID != input.SessionID || reopenedRecord.CommandID != input.CommandID || reopenedRecord.Phase != JobPhaseCreatingSession || reopenedRecord.TeardownState != JobTeardownPending {
		t.Fatalf("reopened job = %+v", reopenedRecord)
	}
	if string(reopenedRecord.CanonicalPayload) != string(input.CanonicalPayload) || string(reopenedRecord.ScriptBytes) != input.Script || string(reopenedRecord.ScriptSHA256) != string(record.ScriptSHA256) {
		t.Fatalf("reopened immutable bytes changed: %+v", reopenedRecord)
	}
}

func TestP024I04SameRunKeyReturnsOriginalAndChangedPayloadConflicts(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/p024-key.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return time.Date(2026, 9, 26, 19, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	input := p024Acceptance(t, "job-p024-key", "session-p024-key", "command-p024-key", "run-key-p024-key", "echo first")
	first, duplicate, err := store.AcceptJob(context.Background(), input)
	if err != nil || duplicate {
		t.Fatalf("first accept = %+v duplicate=%v err=%v", first, duplicate, err)
	}
	retry, duplicate, err := store.AcceptJob(context.Background(), input)
	if err != nil || !duplicate || retry.JobID != first.JobID || retry.SessionID != first.SessionID || retry.CommandID != first.CommandID {
		t.Fatalf("same-key retry = %+v duplicate=%v err=%v", retry, duplicate, err)
	}
	conflict := p024Acceptance(t, "job-p024-conflict", "session-p024-conflict", "command-p024-conflict", input.IdempotencyKey, "echo changed")
	if _, duplicate, err := store.AcceptJob(context.Background(), conflict); !errors.Is(err, ErrIdempotencyConflict) || duplicate {
		t.Fatalf("changed same-key accept = duplicate %v err %v, want conflict", duplicate, err)
	}
	var jobCount, runKeyCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM exec_jobs").Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM exec_idempotency WHERE operation = 'run'").Scan(&runKeyCount); err != nil {
		t.Fatal(err)
	}
	if jobCount != 1 || runKeyCount != 1 {
		t.Fatalf("after retry/conflict jobs=%d run idempotency=%d, want 1/1", jobCount, runKeyCount)
	}
}

func TestP024RejectsNonCanonicalOrScriptMismatchBeforeInsert(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/p024-reject.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	valid := p024Acceptance(t, "job-p024-reject", "session-p024-reject", "command-p024-reject", "run-key-p024-reject", "echo valid")
	nonCanonical := valid
	nonCanonical.CanonicalPayload = []byte(fmt.Sprintf(" {\"script\":%q,\"environment\":%q,\"execution_target\":{\"profile\":%q,\"kind\":%q},\"source\":{\"mode\":\"empty\"},\"operation\":\"run\"} ", valid.Script, valid.Environment, valid.Target.Profile(), valid.Target.Kind()))
	if _, _, err := store.AcceptJob(context.Background(), nonCanonical); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("noncanonical payload error = %v, want ErrInvalidJob", err)
	}
	mismatchedScript := valid
	mismatchedScript.JobID = "job-p024-script-mismatch"
	mismatchedScript.SessionID = "session-p024-script-mismatch"
	mismatchedScript.CommandID = "command-p024-script-mismatch"
	mismatchedScript.IdempotencyKey = "run-key-p024-script-mismatch"
	mismatchedScript.Script = "echo changed outside payload"
	if _, _, err := store.AcceptJob(context.Background(), mismatchedScript); !errors.Is(err, ErrInvalidJob) {
		t.Fatalf("script mismatch error = %v, want ErrInvalidJob", err)
	}
	if _, err := store.GetJob(context.Background(), valid.JobID); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("rejected job lookup = %v, want ErrJobNotFound", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM exec_jobs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected payloads inserted %d jobs", count)
	}
}

func TestP024RejectsStableIdentityCollisions(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/p024-collision.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	first := p024Acceptance(t, "job-p024-collision-1", "session-p024-collision-1", "command-p024-collision-1", "run-key-p024-collision-1", "echo one")
	if _, _, err := store.AcceptJob(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	sessionCollision := p024Acceptance(t, "job-p024-collision-2", string(first.SessionID), "command-p024-collision-2", "run-key-p024-collision-2", "echo two")
	if _, _, err := store.AcceptJob(context.Background(), sessionCollision); !errors.Is(err, ErrJobExists) {
		t.Fatalf("session collision error = %v, want ErrJobExists", err)
	}
	commandCollision := p024Acceptance(t, "job-p024-collision-3", "session-p024-collision-3", string(first.CommandID), "run-key-p024-collision-3", "echo three")
	if _, _, err := store.AcceptJob(context.Background(), commandCollision); !errors.Is(err, ErrJobExists) {
		t.Fatalf("command collision error = %v, want ErrJobExists", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM exec_jobs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("identity collision changed job count to %d, want 1", count)
	}
}

func p024Acceptance(t *testing.T, jobID, sessionID, commandID, key, script string) JobAcceptance {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeDirectMTLS, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	source := domain.NewEmptySource()
	raw := []byte(fmt.Sprintf(`{"operation":"run","environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"source":{"mode":"empty"},"script":%q}`, script))
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return JobAcceptance{
		JobID:                domain.JobID(jobID),
		SessionID:            domain.SessionID(sessionID),
		CommandID:            domain.CommandID(commandID),
		Controller:           controller,
		IdempotencyKey:       key,
		RequestHash:          hash,
		Environment:          "linux-dev",
		Target:               target,
		Source:               source,
		Script:               script,
		CanonicalPayload:     canonical,
		IdempotencyRetention: time.Hour,
	}
}
