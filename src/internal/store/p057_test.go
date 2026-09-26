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

func TestP057LocalIntentPersistsImmutablePayloadAndRequestedLifecycleAcrossRestart(t *testing.T) {
	root := testfixture.New(t)
	databasePath := root.Path() + "/state/p057-restart.db"
	now := time.Date(2026, 9, 26, 21, 0, 0, 0, time.UTC)
	db, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	input := p057CreateSessionIntent(t, "intent-p057-restart", "session-p057-restart", "create-p057-restart")
	record, err := store.CreateLocalIntent(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if record.IntentID != input.IntentID || record.ResourceID != input.ResourceID || record.DeliveryState != LocalIntentRecorded || string(record.PayloadJSON) != string(input.PayloadJSON) || string(record.ScriptBytes) != string(input.ScriptBytes) {
		t.Fatalf("record = %+v, want immutable local receipt", record)
	}
	if !record.CreatedAt.Equal(now) || !record.UpdatedAt.Equal(now) {
		t.Fatalf("timestamps = %s/%s, want %s", record.CreatedAt, record.UpdatedAt, now)
	}
	lifecycle, err := store.ListLocalIntentLifecycle(context.Background(), input.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lifecycle) != 1 || lifecycle[0].NewState != LocalIntentDeliveryState("requested") || lifecycle[0].PreviousState != nil {
		t.Fatalf("initial lifecycle = %+v, want requested", lifecycle)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedDB, err := Open(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedDB.Close()
	reopened, err := NewAuthorityStoreWithClock(reopenedDB, func() time.Time { return now.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	reopenedRecord, err := reopened.GetLocalIntent(context.Background(), input.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if string(reopenedRecord.PayloadJSON) != string(input.PayloadJSON) || string(reopenedRecord.ScriptBytes) != string(input.ScriptBytes) || reopenedRecord.RequestHash.String() != input.RequestHash.String() {
		t.Fatalf("reopened immutable payload changed: %+v", reopenedRecord)
	}
	var count int
	if err := reopenedDB.QueryRow("SELECT COUNT(*) FROM exec_sessions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("local receipt unexpectedly accepted %d target sessions", count)
	}
}

func TestP057LocalIntentRejectsCorruptPayloadBeforeUse(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/p057-corrupt.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	input := p057SubmitIntent(t, "intent-p057-corrupt", "session-p057-corrupt", "command-p057-corrupt", "submit-p057-corrupt", 1, "printf 'exact bytes\\n'")
	if _, err := store.CreateLocalIntent(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE local_intents SET payload_json = ? WHERE intent_id = ?", []byte(`{"operation":"submit_command","script":"different"}`), string(input.IntentID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetLocalIntent(context.Background(), input.IntentID); !errors.Is(err, ErrLocalIntentPayloadCorrupt) {
		t.Fatalf("corrupt payload lookup = %v, want ErrLocalIntentPayloadCorrupt", err)
	}
	if err := store.ValidateLocalIntentPayload(context.Background(), input.IntentID); !errors.Is(err, ErrLocalIntentPayloadCorrupt) {
		t.Fatalf("corrupt payload validation = %v, want ErrLocalIntentPayloadCorrupt", err)
	}
}

func TestP057LocalIntentTransitionKeepsPayloadImmutableAndOrdersLifecycle(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/p057-transition.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStoreWithClock(db, func() time.Time { return time.Date(2026, 9, 26, 22, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	input := p057SubmitIntent(t, "intent-p057-transition", "session-p057-transition", "command-p057-transition", "submit-p057-transition", 1, "echo transition")
	first, err := store.CreateLocalIntent(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionLocalIntent(context.Background(), input.IntentID, LocalIntentDispatching, "lease_acquired"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionLocalIntent(context.Background(), input.IntentID, LocalIntentAccepted, "target_accepted"); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetLocalIntent(context.Background(), input.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if current.DeliveryState != LocalIntentAccepted || string(current.PayloadJSON) != string(first.PayloadJSON) || string(current.ScriptBytes) != string(first.ScriptBytes) {
		t.Fatalf("transition changed immutable request: %+v", current)
	}
	lifecycle, err := store.ListLocalIntentLifecycle(context.Background(), input.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lifecycle) != 3 || lifecycle[1].NewState != LocalIntentDispatching || lifecycle[2].NewState != LocalIntentAccepted || lifecycle[2].PreviousState == nil || *lifecycle[2].PreviousState != LocalIntentDispatching {
		t.Fatalf("lifecycle = %+v", lifecycle)
	}
	if _, err := store.TransitionLocalIntent(context.Background(), input.IntentID, LocalIntentRecorded, "backward"); !errors.Is(err, ErrLocalIntentTransition) {
		t.Fatalf("backward transition = %v, want ErrLocalIntentTransition", err)
	}
}

func TestP057LocalIntentCommandOrdinalIsUnique(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/p057-ordinal.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	first := p057SubmitIntent(t, "intent-p057-order-1", "session-p057-order", "command-p057-order-1", "key-p057-order-1", 1, "echo one")
	if _, err := store.CreateLocalIntent(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := p057SubmitIntent(t, "intent-p057-order-2", "session-p057-order", "command-p057-order-2", "key-p057-order-2", 1, "echo two")
	if _, err := store.CreateLocalIntent(context.Background(), second); !errors.Is(err, ErrLocalIntentExists) {
		t.Fatalf("duplicate intent ordinal = %v, want ErrLocalIntentExists", err)
	}
}

func p057CreateSessionIntent(t *testing.T, intentID, sessionID, key string) LocalIntentCreate {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(fmt.Sprintf(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"operation":"create_session","session_id":%q}`, sessionID))
	canonical, err := domain.CanonicalizeMutationRequestJSON("create_session", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("create_session", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return LocalIntentCreate{IntentID: domain.IntentID(intentID), Operation: "create_session", ResourceID: sessionID, SessionID: domain.SessionID(sessionID), Target: target, Environment: "mac-dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: key, PayloadJSON: canonical}
}

func p057SubmitIntent(t *testing.T, intentID, sessionID, commandID, key string, ordinal int64, script string) LocalIntentCreate {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeQueuedMac, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(fmt.Sprintf(`{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"},"operation":"submit_command","script":%q,"session_id":%q}`, script, sessionID))
	canonical, err := domain.CanonicalizeMutationRequestJSON("submit_command", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return LocalIntentCreate{IntentID: domain.IntentID(intentID), Operation: "submit_command", ResourceID: commandID, SessionID: domain.SessionID(sessionID), CommandID: domain.CommandID(commandID), Target: target, Environment: "linux-dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: key, PayloadJSON: canonical, ScriptBytes: []byte(script), IntentOrdinal: &ordinal}
}
