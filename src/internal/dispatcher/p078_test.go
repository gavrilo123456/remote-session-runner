package dispatcher

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestP078LocalDriverRoutesOneOffRunWithStableIDs(t *testing.T) {
	authority := p068Authority(t)
	intent := p078RunIntent(t, "intent-p078-local", "job-p078-local", "session-p078-local", "command-p078-local", domain.TargetKindLocal)
	if _, err := authority.CreateLocalIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	acceptor := &p068FakeAcceptor{operation: "run"}
	driver, err := NewLocalDriver(authority, acceptor, "router-p078", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	record, acceptance, err := driver.DispatchIntent(context.Background(), intent.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	if record.DeliveryState != store.LocalIntentAccepted || acceptance.Operation != "run" || acceptance.ResourceID != string(intent.JobID) {
		t.Fatalf("run dispatch = %+v/%+v", record, acceptance)
	}
	if len(acceptor.requests) != 1 {
		t.Fatalf("acceptor requests = %+v", acceptor.requests)
	}
	if _, _, err := driver.DispatchIntent(context.Background(), intent.IntentID); err == nil {
		t.Fatal("accepted run was dispatched a second time")
	}
	if len(acceptor.requests) != 1 {
		t.Fatalf("retry changed target call count = %d", len(acceptor.requests))
	}
}

func p078RunIntent(t *testing.T, intentID, jobID, sessionID, commandID string, targetKind domain.TargetKind) store.LocalIntentCreate {
	t.Helper()
	profile := "mac-workstation"
	controllerType := domain.ControllerTypeLocalUser
	if targetKind == domain.TargetKindRemote {
		profile = "linux-host"
		controllerType = domain.ControllerTypeQueuedMac
	}
	target, err := domain.NewExecutionTarget(targetKind, profile)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(controllerType, "tomasz.walczuk")
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"operation": "run", "environment": "dev", "execution_target": map[string]string{"kind": string(targetKind), "profile": profile}, "job_id": jobID, "session_id": sessionID, "command_id": commandID, "script": "echo exact", "source": map[string]string{"mode": "empty"}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON("run", raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("run", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store.LocalIntentCreate{IntentID: domain.IntentID(intentID), Operation: "run", ResourceID: jobID, SessionID: domain.SessionID(sessionID), CommandID: domain.CommandID(commandID), JobID: domain.JobID(jobID), Target: target, Environment: "dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash, IdempotencyKey: "key-" + intentID, PayloadJSON: canonical, ScriptBytes: []byte("echo exact")}
}
