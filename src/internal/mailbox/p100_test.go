package mailbox

import "testing"

func TestP100RunResponseShapesMatchV1Schema(t *testing.T) {
	compiled := p004MailboxSchemas(t)
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "queued local intent",
			raw:  `{"request_id":"req-p100-accepted","operation":"run","request_state":"accepted","response_revision":1,"job_id":"job-p100","session_id":"sess-p100","command_id":"cmd-p100","delivery_state":"recorded"}`,
		},
		{
			name: "terminal command includes teardown",
			raw:  `{"request_id":"req-p100-complete","operation":"run","request_state":"complete","response_revision":2,"job_id":"job-p100","job_phase":"complete","session_id":"sess-p100","command_id":"cmd-p100","delivery_state":"accepted","command_state":"succeeded","observed_at":"2026-09-27T12:00:00Z","exit_code":0,"final_event_sequence":4,"available_event_sequence":4,"output_complete":true,"output_truncated":false,"events_file":"events/cmd-p100.ndjson","teardown_outcome":"closed"}`,
		},
		{
			name: "proven never delivered stays intent only",
			raw:  `{"request_id":"req-p100-never","operation":"run","request_state":"rejected","response_revision":2,"job_id":"job-p100-never","session_id":"sess-p100-never","command_id":"cmd-p100-never","delivery_state":"not_delivered","error":{"code":"runtime_unavailable","message":"run was proven not delivered","retryable":false}}`,
		},
		{
			name: "accepted target with unavailable terminal status",
			raw:  `{"request_id":"req-p100-status","operation":"run","request_state":"indeterminate","response_revision":2,"job_id":"job-p100-status","session_id":"sess-p100-status","command_id":"cmd-p100-status","delivery_state":"accepted","error":{"code":"remote_status_unavailable","message":"remote target accepted the request but its terminal status could not be verified","retryable":false}}`,
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
