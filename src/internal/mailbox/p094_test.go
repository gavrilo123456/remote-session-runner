package mailbox

import "testing"

func TestP094SessionResponseShapesMatchV1Schema(t *testing.T) {
	compiled := p004MailboxSchemas(t)
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "accepted create intent",
			raw:  `{"request_id":"req-p094-create","operation":"create_session","request_state":"accepted","response_revision":1,"session_id":"sess-p094","delivery_state":"recorded"}`,
		},
		{
			name: "active local intent read",
			raw:  `{"request_id":"req-p094-get","operation":"get_session","request_state":"complete","response_revision":1,"session_id":"sess-p094","delivery_state":"recorded","observed_at":"2026-09-27T12:00:00Z"}`,
		},
		{
			name: "ready projection read",
			raw:  `{"request_id":"req-p094-ready","operation":"get_session","request_state":"complete","response_revision":2,"session_id":"sess-p094","session_state":"ready","observed_at":"2026-09-27T12:00:00Z"}`,
		},
		{
			name: "direct-created session denial",
			raw:  `{"request_id":"req-p094-denied","operation":"get_session","request_state":"rejected","response_revision":1,"error":{"code":"resource_not_found","message":"session is not available through this Mac ingress","retryable":false}}`,
		},
		{
			name: "proven never-delivered create",
			raw:  `{"request_id":"req-p094-undelivered","operation":"create_session","request_state":"rejected","response_revision":2,"session_id":"sess-p094","delivery_state":"not_delivered","error":{"code":"resource_not_found","message":"session creation was proven not delivered","retryable":false}}`,
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
