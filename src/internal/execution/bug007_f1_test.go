package execution

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestBUG007F1ResumeCommandExecutesForeignSchedulerClaim(t *testing.T) {
	runtime := &p020FakeRuntime{
		generation:    "generation-bug007-f1-cross-claim",
		stopConfirmed: true,
		commandResult: RuntimeCommandResult{ExitCode: 0},
	}
	service, authority, _ := newP020Service(t, runtime)
	olderSession := p021ReadySession(t, service, "session-bug007-f1-a-older", "key-bug007-f1-a-session")
	requestedSession := p021ReadySession(t, service, "session-bug007-f1-b-requested", "key-bug007-f1-b-session")
	older := p022QueueCommand(t, authority, olderSession, "command-bug007-f1-a-older", "key-bug007-f1-a-command")
	requested := p022QueueCommand(t, authority, requestedSession, "command-bug007-f1-b-requested", "key-bug007-f1-b-command")

	result, err := service.ResumeCommand(context.Background(), requested.CommandID, requestedSession.Controller)
	if err != nil {
		t.Fatal(err)
	}
	if result.Command.CommandID != requested.CommandID || result.Command.State != domain.CommandStateQueued {
		t.Fatalf("requested resume result = %+v, want queued %s", result.Command, requested.CommandID)
	}
	completed, err := authority.GetCommand(context.Background(), older.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != domain.CommandStateSucceeded {
		t.Fatalf("foreign scheduler claim = %+v, want succeeded", completed)
	}
	if runtime.commandCall != 1 {
		t.Fatalf("runtime command calls = %d, want 1", runtime.commandCall)
	}
	events, err := authority.ListCommandEvents(context.Background(), older.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	wantTypes := []string{"command_queued", "command_started", "command_succeeded"}
	if len(events) != len(wantTypes) {
		t.Fatalf("foreign command events = %+v, want %v", events, wantTypes)
	}
	for index, want := range wantTypes {
		if events[index].Type != want {
			t.Fatalf("foreign command event %d = %q, want %q", index, events[index].Type, want)
		}
	}
	if live, err := authority.CountLiveCommandSlots(context.Background()); err != nil || live != 0 {
		t.Fatalf("live command slots = %d, err = %v, want 0", live, err)
	}
}

func TestBUG007F1ClaimedCommandExecutesExactlyOnce(t *testing.T) {
	runtime := &p020FakeRuntime{
		generation:    "generation-bug007-f1-claim",
		stopConfirmed: true,
		commandResult: RuntimeCommandResult{ExitCode: 0},
	}
	service, authority, _ := newP020Service(t, runtime)
	session := p021ReadySession(t, service, "session-bug007-f1-claim", "key-bug007-f1-claim-session")
	queued := p022QueueCommand(t, authority, session, "command-bug007-f1-claim", "key-bug007-f1-claim-command")

	claim, err := service.ClaimNextEligibleCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if claim.CommandID != queued.CommandID || claim.State != domain.CommandStateRunning {
		t.Fatalf("scheduler claim = %+v, want running %s", claim, queued.CommandID)
	}
	if live, err := authority.CountLiveCommandSlots(context.Background()); err != nil || live != 1 {
		t.Fatalf("live command slots after claim = %d, err = %v, want 1", live, err)
	}

	completed, err := service.ExecuteClaimedCommand(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Command.State != domain.CommandStateSucceeded {
		t.Fatalf("claimed execution = %+v, want succeeded", completed.Command)
	}
	replayed, err := service.ExecuteClaimedCommand(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Command.State != domain.CommandStateSucceeded {
		t.Fatalf("replayed claimed execution = %+v, want succeeded", replayed.Command)
	}
	if runtime.commandCall != 1 {
		t.Fatalf("runtime command calls = %d, want 1", runtime.commandCall)
	}
	events, err := authority.ListCommandEvents(context.Background(), queued.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[1].Type != "command_started" || events[2].Type != "command_succeeded" {
		t.Fatalf("claimed command events = %+v, want queued/started/succeeded", events)
	}
	if live, err := authority.CountLiveCommandSlots(context.Background()); err != nil || live != 0 {
		t.Fatalf("live command slots after completion = %d, err = %v, want 0", live, err)
	}
}

func TestBUG007F1ResumeStoredJobUsesCanonicalPolicy(t *testing.T) {
	runtime := &p025Runtime{p020FakeRuntime: &p020FakeRuntime{
		generation:    "generation-bug007-f1-stored-policy",
		stopConfirmed: true,
		commandResult: RuntimeCommandResult{ExitCode: 0},
	}}
	service, authority, _ := newP025Service(t, runtime)
	request := bug007F1StoredPolicyRequest(t, "job-bug007-f1-stored-policy", "session-bug007-f1-stored-policy", "command-bug007-f1-stored-policy", "run-bug007-f1-stored-policy", "echo stored policy")
	accepted, duplicate, err := authority.AcceptJob(context.Background(), request.Acceptance)
	if err != nil || duplicate {
		t.Fatalf("accept stored job = %+v duplicate=%v err=%v", accepted, duplicate, err)
	}

	resumed, err := service.ResumeStoredJob(context.Background(), accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Job.Phase != store.JobPhaseComplete || resumed.Command.State != domain.CommandStateSucceeded || resumed.Session.State != domain.SessionStateClosed {
		t.Fatalf("resumed stored job = %+v", resumed)
	}
	if resumed.Session.Limits.CommandTimeout != 42*time.Second {
		t.Fatalf("stored command timeout = %s, want 42s", resumed.Session.Limits.CommandTimeout)
	}
	if runtime.prepareCall != 1 || runtime.startCall != 1 || runtime.commandCall != 1 {
		t.Fatalf("runtime calls prepare=%d start=%d command=%d, want 1/1/1", runtime.prepareCall, runtime.startCall, runtime.commandCall)
	}
}

func bug007F1StoredPolicyRequest(t *testing.T, jobID, sessionID, commandID, key, script string) RunJobRequest {
	t.Helper()
	request := p025Request(t, jobID, sessionID, commandID, key, script)
	requestedLimits := domain.RequestedLimits{CommandTimeout: 42 * time.Second}
	raw, err := json.Marshal(map[string]any{
		"operation":   "run",
		"environment": "linux-dev",
		"execution_target": map[string]string{
			"kind":    "remote",
			"profile": "linux-host",
		},
		"source":           map[string]string{"mode": "empty"},
		"script":           script,
		"requested_limits": requestedLimits,
		"isolation":        domain.IsolationRequirements{},
	})
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
	request.Acceptance.CanonicalPayload = canonical
	request.Acceptance.RequestHash = hash
	return request
}
