package runnerlocald

import (
	"context"
	"fmt"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

// TestBUG019MacPolicyLoopSettlesAClaimWithNoRuntimeWorker covers the precise
// unsafe gap from the report: after a durable claim, no in-memory worker is
// required for the configured deadline to produce one terminal result. The
// command body is deliberately never executed.
func TestBUG019MacPolicyLoopSettlesAClaimWithNoRuntimeWorker(t *testing.T) {
	authority, service, runtime := newBUG011P4Service(t, true)
	server, err := NewPrivateServer(PrivateServerOptions{Authority: authority, Service: service, SocketPath: p060SocketPath(t)})
	if err != nil {
		t.Fatal(err)
	}
	create := p060CreateIntent(t, authority, "intent-bug019-create", "session-bug019-policy", "key-bug019-create")
	if _, err := server.acceptIntent(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	submitInput := bug019TimedSubmitIntent(t, "intent-bug019-command", string(create.SessionID), "command-bug019-policy", "key-bug019-command")
	submit, err := authority.CreateLocalIntent(context.Background(), submitInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.acceptIntent(context.Background(), submit); err != nil {
		t.Fatal(err)
	}
	claim, err := service.ClaimNextEligibleCommand(context.Background())
	if err != nil || claim.CommandID != domain.CommandID("command-bug019-policy") {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		enforceMacLocalPolicies(ctx, service, 5*time.Millisecond)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		command, readErr := authority.GetCommand(context.Background(), claim.CommandID)
		if readErr == nil && command.State == domain.CommandStateTimedOut {
			if !command.OutputComplete || command.FinalEventSequence == nil {
				t.Fatalf("timed command lacks complete terminal boundary: %+v", command)
			}
			if runtime.executionCount(claim.CommandID) != 0 {
				t.Fatalf("expired claim replayed command body %d times", runtime.executionCount(claim.CommandID))
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	command, readErr := authority.GetCommand(context.Background(), claim.CommandID)
	t.Fatalf("stale durable claim did not settle: command=%+v err=%v", command, readErr)
}

func bug019TimedSubmitIntent(t *testing.T, intentID, sessionID, commandID, key string) store.LocalIntentCreate {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindLocal, "mac-workstation")
	if err != nil {
		t.Fatal(err)
	}
	controller, err := domain.NewControllerIdentity(domain.ControllerTypeLocalUser, domain.ControllerID("tomasz.walczuk"))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(fmt.Sprintf(`{"environment":"mac-dev","execution_target":{"kind":"local","profile":"mac-workstation"},"operation":"submit_command","script":"printf bounded","session_id":%q,"timeout_seconds":1}`, sessionID))
	canonical, err := domain.CanonicalizeMutationRequestJSON("submit_command", payload, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON("submit_command", canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return store.LocalIntentCreate{
		IntentID: domain.IntentID(intentID), Operation: "submit_command", ResourceID: commandID,
		SessionID: domain.SessionID(sessionID), CommandID: domain.CommandID(commandID), Target: target,
		Environment: "mac-dev", Controller: controller, Source: domain.NewEmptySource(), RequestHash: hash,
		IdempotencyKey: key, PayloadJSON: canonical, ScriptBytes: []byte("printf bounded"),
	}
}
