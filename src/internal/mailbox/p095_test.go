package mailbox

import "testing"

func TestP095CommandResponseShapesMatchV1Schema(t *testing.T) {
	compiled := p004MailboxSchemas(t)
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "accepted local command intent",
			raw:  `{"request_id":"req-p095-submit","operation":"submit_command","request_state":"accepted","response_revision":1,"command_id":"command-p095","session_id":"sess-p095","delivery_state":"recorded"}`,
		},
		{
			name: "uncertain delivery has no command outcome",
			raw:  `{"request_id":"req-p095-uncertain","operation":"submit_command","request_state":"accepted","response_revision":2,"command_id":"command-p095","session_id":"sess-p095","delivery_state":"uncertain","observed_at":"2026-09-27T12:00:00Z"}`,
		},
		{
			name: "nonzero shell exit is a completed failed command",
			raw:  `{"request_id":"req-p095-failed","operation":"submit_command","request_state":"complete","response_revision":2,"command_id":"command-p095","session_id":"sess-p095","delivery_state":"accepted","command_state":"failed","observed_at":"2026-09-27T12:00:00Z","exit_code":7,"final_event_sequence":4,"available_event_sequence":4,"output_complete":true,"output_truncated":false,"events_file":"events/command-p095.ndjson"}`,
		},
		{
			name: "active get is a frozen incomplete snapshot",
			raw:  `{"request_id":"req-p095-active","operation":"get_command","request_state":"complete","response_revision":1,"command_id":"command-p095","session_id":"sess-p095","command_state":"queued","observed_at":"2026-09-27T12:00:00Z","available_event_sequence":1,"output_complete":false,"output_truncated":false,"events_file":"events/command-p095.ndjson"}`,
		},
		{
			name: "queued remote accepted submit has no target state",
			raw:  `{"request_id":"req-p095-remote","operation":"submit_command","request_state":"accepted","response_revision":1,"command_id":"command-p095-remote","session_id":"sess-p095-remote","delivery_state":"recorded"}`,
		},
		{
			name: "direct-created command is denied",
			raw:  `{"request_id":"req-p095-denied","operation":"get_command","request_state":"rejected","response_revision":1,"error":{"code":"resource_not_found","message":"command is not available through this Mac ingress","retryable":false}}`,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value, err := p004MailboxDecode([]byte(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if err := compiled["response"].Validate(value); err != nil {
				t.Fatalf("response schema validation failed: %v", err)
			}
			if err := p004MailboxSemantic("response", value); err != nil {
				t.Fatalf("response semantic validation failed: %v", err)
			}
		})
	}
}
