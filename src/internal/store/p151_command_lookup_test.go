package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/testfixture"
)

func TestP151GetLocalIntentByCommandReturnsExactRemoteSubmitAndRun(t *testing.T) {
	ctx := context.Background()
	authority := p151CommandLookupAuthority(t)
	controller := p151CommandLookupController(t, domain.ControllerTypeQueuedMac, "tomasz.walczuk")

	submit := p151RemoteSubmitIntent(t, "intent-p151-submit", "session-p151-submit", "command-p151-submit", "key-p151-submit", controller)
	run := p151RemoteRunIntent(t, "intent-p151-run", "job-p151-run", "session-p151-run", "command-p151-run", "key-p151-run", controller)
	for _, input := range []LocalIntentCreate{submit, run} {
		if _, err := authority.CreateLocalIntent(ctx, input); err != nil {
			t.Fatalf("create %s intent: %v", input.Operation, err)
		}
	}

	for _, want := range []LocalIntentCreate{submit, run} {
		got, err := authority.GetLocalIntentByCommand(ctx, want.CommandID, controller)
		if err != nil {
			t.Fatalf("lookup %s command: %v", want.Operation, err)
		}
		if got.IntentID != want.IntentID || got.Operation != want.Operation || got.CommandID != want.CommandID || got.Controller != controller || got.Target.Kind() != domain.TargetKindRemote || got.Target.Profile() != "linux-host" {
			t.Fatalf("lookup %s command = %+v, want intent=%q command=%q controller=%+v remote/linux-host", want.Operation, got, want.IntentID, want.CommandID, controller)
		}
	}
}

func TestP151GetLocalIntentByCommandIsolatesControllersWithSameCommandID(t *testing.T) {
	ctx := context.Background()
	authority := p151CommandLookupAuthority(t)
	commandID := "command-p151-controller-isolation"
	queued := p151CommandLookupController(t, domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	direct := p151CommandLookupController(t, domain.ControllerTypeDirectMTLS, "tomasz.walczuk")

	queuedInput := p151RemoteRunIntent(t, "intent-p151-queued", "job-p151-queued", "session-p151-queued", commandID, "key-p151-queued", queued)
	directInput := p151RemoteRunIntent(t, "intent-p151-direct", "job-p151-direct", "session-p151-direct", commandID, "key-p151-direct", direct)
	for _, input := range []LocalIntentCreate{queuedInput, directInput} {
		if _, err := authority.CreateLocalIntent(ctx, input); err != nil {
			t.Fatalf("create %s intent: %v", input.IntentID, err)
		}
	}

	for _, want := range []LocalIntentCreate{queuedInput, directInput} {
		got, err := authority.GetLocalIntentByCommand(ctx, want.CommandID, want.Controller)
		if err != nil {
			t.Fatalf("lookup controller %s/%s: %v", want.Controller.Type(), want.Controller.ID(), err)
		}
		if got.IntentID != want.IntentID || got.Controller != want.Controller {
			t.Fatalf("lookup controller %s/%s = %+v, want intent=%q controller=%+v", want.Controller.Type(), want.Controller.ID(), got, want.IntentID, want.Controller)
		}
	}

	other := p151CommandLookupController(t, domain.ControllerTypeQueuedMac, "other-mac-user")
	if _, err := authority.GetLocalIntentByCommand(ctx, domain.CommandID(commandID), other); !errors.Is(err, ErrLocalIntentNotFound) {
		t.Fatalf("lookup other controller error = %v, want ErrLocalIntentNotFound", err)
	}
}

func TestP151GetLocalIntentByCommandRejectsMissingAndInvalidInputs(t *testing.T) {
	ctx := context.Background()
	authority := p151CommandLookupAuthority(t)
	controller := p151CommandLookupController(t, domain.ControllerTypeQueuedMac, "tomasz.walczuk")

	if _, err := authority.GetLocalIntentByCommand(ctx, domain.CommandID("command-p151-missing"), controller); !errors.Is(err, ErrLocalIntentNotFound) {
		t.Fatalf("missing command lookup error = %v, want ErrLocalIntentNotFound", err)
	}
	if _, err := authority.GetLocalIntentByCommand(ctx, "", controller); !errors.Is(err, ErrInvalidLocalIntent) {
		t.Fatalf("empty command lookup error = %v, want ErrInvalidLocalIntent", err)
	}
	if _, err := authority.GetLocalIntentByCommand(ctx, "command-p151-invalid-controller", domain.ControllerIdentity{}); !errors.Is(err, ErrInvalidLocalIntent) {
		t.Fatalf("invalid controller lookup error = %v, want ErrInvalidLocalIntent", err)
	}
}

func TestP151GetLocalIntentByCommandFailsClosedForDuplicateControllerBinding(t *testing.T) {
	ctx := context.Background()
	authority := p151CommandLookupAuthority(t)
	controller := p151CommandLookupController(t, domain.ControllerTypeQueuedMac, "tomasz.walczuk")
	commandID := "command-p151-ambiguous"

	first := p151RemoteRunIntent(t, "intent-p151-ambiguous-a", "job-p151-ambiguous-a", "session-p151-ambiguous-a", commandID, "key-p151-ambiguous-a", controller)
	second := p151RemoteRunIntent(t, "intent-p151-ambiguous-b", "job-p151-ambiguous-b", "session-p151-ambiguous-b", commandID, "key-p151-ambiguous-b", controller)
	for _, input := range []LocalIntentCreate{first, second} {
		if _, err := authority.CreateLocalIntent(ctx, input); err != nil {
			t.Fatalf("create duplicate binding %s: %v", input.IntentID, err)
		}
	}

	if _, err := authority.GetLocalIntentByCommand(ctx, domain.CommandID(commandID), controller); !errors.Is(err, ErrLocalIntentPayloadCorrupt) {
		t.Fatalf("duplicate command lookup error = %v, want ErrLocalIntentPayloadCorrupt", err)
	}
}

func p151CommandLookupAuthority(t *testing.T) *AuthorityStore {
	t.Helper()
	root := testfixture.New(t)
	database, err := Open(context.Background(), filepath.Join(root.Path(), "state", "p151-command-lookup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	authority, err := NewAuthorityStore(database)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func p151CommandLookupController(t *testing.T, kind domain.ControllerType, id string) domain.ControllerIdentity {
	t.Helper()
	controller, err := domain.NewControllerIdentity(kind, domain.ControllerID(id))
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func p151RemoteSubmitIntent(t *testing.T, intentID, sessionID, commandID, key string, controller domain.ControllerIdentity) LocalIntentCreate {
	t.Helper()
	target := p151RemoteTarget(t)
	script := "printf p151-submit"
	canonical, hash := p151CanonicalIntentPayload(t, "submit_command", map[string]any{
		"operation":        "submit_command",
		"environment":      "linux-dev",
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
		"source":           map[string]string{"mode": "empty"},
		"session_id":       sessionID,
		"command_id":       commandID,
		"script":           script,
	})
	return LocalIntentCreate{
		IntentID: domain.IntentID(intentID), Operation: "submit_command", ResourceID: commandID,
		SessionID: domain.SessionID(sessionID), CommandID: domain.CommandID(commandID),
		Target: target, Environment: "linux-dev", Controller: controller, Source: domain.NewEmptySource(),
		RequestHash: hash, IdempotencyKey: key, PayloadJSON: canonical, ScriptBytes: []byte(script),
	}
}

func p151RemoteRunIntent(t *testing.T, intentID, jobID, sessionID, commandID, key string, controller domain.ControllerIdentity) LocalIntentCreate {
	t.Helper()
	target := p151RemoteTarget(t)
	script := "printf p151-run"
	canonical, hash := p151CanonicalIntentPayload(t, "run", map[string]any{
		"operation":        "run",
		"environment":      "linux-dev",
		"execution_target": map[string]string{"kind": "remote", "profile": "linux-host"},
		"source":           map[string]string{"mode": "empty"},
		"job_id":           jobID,
		"session_id":       sessionID,
		"command_id":       commandID,
		"script":           script,
	})
	return LocalIntentCreate{
		IntentID: domain.IntentID(intentID), Operation: "run", ResourceID: jobID,
		JobID: domain.JobID(jobID), SessionID: domain.SessionID(sessionID), CommandID: domain.CommandID(commandID),
		Target: target, Environment: "linux-dev", Controller: controller, Source: domain.NewEmptySource(),
		RequestHash: hash, IdempotencyKey: key, PayloadJSON: canonical, ScriptBytes: []byte(script),
	}
}

func p151RemoteTarget(t *testing.T) domain.ExecutionTarget {
	t.Helper()
	target, err := domain.NewExecutionTarget(domain.TargetKindRemote, "linux-host")
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func p151CanonicalIntentPayload(t *testing.T, operation string, payload map[string]any) ([]byte, domain.CanonicalHash) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := domain.CanonicalizeMutationRequestJSON(operation, raw, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashMutationRequestJSON(operation, canonical, domain.CanonicalizationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return canonical, hash
}
