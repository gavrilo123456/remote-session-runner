package mailbox

import (
	"encoding/json"
	"testing"

	"remote-session-runner/src/internal/domain"
	"remote-session-runner/src/internal/store"
)

func TestBUG008MailboxActiveRunProjectionIsNarrowAndStable(t *testing.T) {
	queued := domain.CommandStateQueued
	snapshot := RunSnapshot{
		JobID: "job-bug008-mailbox", SessionID: "sess-bug008-mailbox", CommandID: "cmd-bug008-mailbox",
		DeliveryState: string(store.LocalIntentAccepted),
		ActiveRunProjection: &ActiveRunProjection{
			JobPhase: store.JobPhaseAwaitingCommand, CommandState: &queued,
		},
	}
	if err := validateActiveRunProjection(snapshot); err != nil {
		t.Fatalf("valid active remote projection rejected: %v", err)
	}
	response := acceptedRunProgressResponse("req-bug008-mailbox", snapshot)
	// publishRunResponse assigns the positive revision in production. Set it
	// here before direct schema validation so this unit test exercises the
	// active-receipt shape rather than the publisher's revision responsibility.
	response.ResponseRevision = 1
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	value, err := p004MailboxDecode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := p004MailboxSchemas(t)["response"].Validate(value); err != nil {
		t.Fatalf("active remote response does not match the mailbox schema: %v", err)
	}
	if err := p004MailboxSemantic("response", value); err != nil {
		t.Fatalf("active remote response does not match mailbox semantics: %v", err)
	}
	for _, field := range []string{
		"observed_at", "exit_code", "stdout", "stderr", "final_event_sequence", "available_event_sequence",
		"output_complete", "output_truncated", "output_unavailable_reason", "events_file", "teardown_outcome", "error",
	} {
		if _, present := wire[field]; present {
			t.Fatalf("active remote response exposed %q: %s", field, raw)
		}
	}
	if !sameAcceptedRunProgress(response, snapshot) {
		t.Fatalf("same active projection was not recognized as stable: %+v", response)
	}

	changed := snapshot
	running := domain.CommandStateRunning
	changed.ActiveRunProjection = &ActiveRunProjection{JobPhase: store.JobPhaseAwaitingCommand, CommandState: &running}
	if sameAcceptedRunProgress(response, changed) {
		t.Fatal("changed active command state did not require a new revision")
	}

	terminal := domain.CommandStateSucceeded
	invalid := snapshot
	invalid.ActiveRunProjection = &ActiveRunProjection{JobPhase: store.JobPhaseComplete, CommandState: &terminal}
	if err := validateActiveRunProjection(invalid); err == nil {
		t.Fatal("terminal remote projection was accepted as active")
	}
}

func TestBUG008MailboxActiveRunProjectionAllowsSafeNonterminalPhases(t *testing.T) {
	queued := domain.CommandStateQueued
	for _, fixture := range []struct {
		name  string
		phase store.JobPhase
		state *domain.CommandState
	}{
		{name: "creating session", phase: store.JobPhaseCreatingSession},
		{name: "accepting command", phase: store.JobPhaseAcceptingCommand},
		{name: "awaiting queued command", phase: store.JobPhaseAwaitingCommand, state: &queued},
		{name: "closing session", phase: store.JobPhaseClosingSession},
	} {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			snapshot := RunSnapshot{
				JobID: "job-bug008-phase", SessionID: "sess-bug008-phase", CommandID: "cmd-bug008-phase",
				DeliveryState: string(store.LocalIntentAccepted),
				ActiveRunProjection: &ActiveRunProjection{
					JobPhase: fixture.phase, CommandState: fixture.state,
				},
			}
			if err := validateActiveRunProjection(snapshot); err != nil {
				t.Fatalf("valid %s projection rejected: %v", fixture.name, err)
			}
			response := acceptedRunProgressResponse("req-bug008-phase", snapshot)
			response.ResponseRevision = 1
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			value, err := p004MailboxDecode(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := p004MailboxSchemas(t)["response"].Validate(value); err != nil {
				t.Fatalf("active %s response does not match schema: %v", fixture.name, err)
			}
			if err := p004MailboxSemantic("response", value); err != nil {
				t.Fatalf("active %s response does not match semantics: %v", fixture.name, err)
			}
		})
	}
}

func TestBUG008AcceptedReceiptSchemaRejectsResultFieldsWithoutActiveProjection(t *testing.T) {
	schema := p004MailboxSchemas(t)["response"]
	for field, fieldValue := range map[string]any{
		"exit_code":                 0,
		"stdout":                    "must-not-appear",
		"stderr":                    "must-not-appear",
		"final_event_sequence":      1,
		"available_event_sequence":  1,
		"output_complete":           false,
		"output_truncated":          false,
		"output_unavailable_reason": "remote_event_gap",
		"events_file":               "events/cmd-bug008-mailbox.ndjson",
		"teardown_outcome":          "closed",
		"error": map[string]any{
			"code": "runtime_unavailable", "message": "must-not-appear", "retryable": false,
		},
	} {
		t.Run(field, func(t *testing.T) {
			document := map[string]any{
				"request_id": "req-bug008-mailbox", "operation": "run", "request_state": "accepted", "response_revision": 1,
				"job_id": "job-bug008-mailbox", "session_id": "sess-bug008-mailbox", "command_id": "cmd-bug008-mailbox",
				"delivery_state": "accepted", field: fieldValue,
			}
			raw, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			value, err := p004MailboxDecode(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(value); err == nil {
				t.Fatalf("accepted generic receipt accepted result field %q: %s", field, raw)
			}
		})
	}
}
