package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP104LocalTargetAcceptanceAllowsNextIntentToReachAuthority(t *testing.T) {
	root := testfixture.New(t)
	db, err := Open(context.Background(), root.Path()+"/state/p104-local-order.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	authority, err := NewAuthorityStore(db)
	if err != nil {
		t.Fatal(err)
	}
	first, err := authority.CreateLocalIntent(context.Background(), p104LocalSubmitIntent(t, "intent-p104-local-1", "session-p104-local", "command-p104-local-1", "key-p104-local-1", "echo one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := authority.CreateLocalIntent(context.Background(), p104LocalSubmitIntent(t, "intent-p104-local-2", "session-p104-local", "command-p104-local-2", "key-p104-local-2", "echo two"))
	if err != nil {
		t.Fatal(err)
	}
	if first.IntentOrdinal == nil || *first.IntentOrdinal != 1 || second.IntentOrdinal == nil || *second.IntentOrdinal != 2 {
		t.Fatalf("local intent ordinals first=%v second=%v", first.IntentOrdinal, second.IntentOrdinal)
	}
	if _, err := authority.ClaimLocalIntent(context.Background(), first.IntentID, "router-p104", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.TransitionLocalIntent(context.Background(), first.IntentID, LocalIntentAccepted, "local_authority_accepted"); err != nil {
		t.Fatal(err)
	}
	claimed, err := authority.ClaimLocalIntent(context.Background(), second.IntentID, "router-p104", time.Minute)
	if err != nil || claimed.IntentID != second.IntentID || claimed.DeliveryState != LocalIntentDispatching {
		t.Fatalf("second local intent claim=%+v err=%v; local authority should enforce its own command order", claimed, err)
	}
}

func p104LocalSubmitIntent(t *testing.T, intentID, sessionID, commandID, key, script string) LocalIntentCreate {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(fmt.Sprintf(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"session_id":%q,"script":%q}`, sessionID, script))
	canonical, err := domain.CanonicalizeMutationRequestJSON("submit_command", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return LocalIntentCreate{
		IntentID: domain.IntentID(intentID), Operation: "submit_command", ResourceID: commandID,
		SessionID: domain.SessionID(sessionID), CommandID: domain.CommandID(commandID), Target: target,
		Environment: "mac-dev", Controller: controller, Source: domain.NewEmptySource(),
		RequestHash: hash, IdempotencyKey: key, PayloadJSON: canonical, ScriptBytes: []byte(script),
	}
}
